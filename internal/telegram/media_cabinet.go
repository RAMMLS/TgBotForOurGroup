package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	telebot "gopkg.in/telebot.v3"

	mediafeature "tgbotforourgroup/internal/features/media"
	"tgbotforourgroup/internal/storage"
)

const mediaCallbackUnique = "mediaact"
const mediaCallbackPrefix = "\f" + mediaCallbackUnique + "|"

type pendingMediaImport struct {
	Summary       string
	SuggestedName string
	SourceType    string
	SourceRef     string
	Tags          []string
	Items         []storage.MediaItem
}

func (s *Service) registerMediaCabinetHandlers() {
	s.bot.Handle("/media", func(c telebot.Context) error {
		if !s.isPrivateChat(c.Chat()) {
			return c.Send("Открой эту команду в личном чате с ботом.")
		}
		return s.sendMediaCabinetHome(c, "")
	})

	s.bot.Handle(telebot.OnSticker, func(c telebot.Context) error {
		return s.handlePrivateMediaImport(c)
	})
	s.bot.Handle(telebot.OnAnimation, func(c telebot.Context) error {
		return s.handlePrivateMediaImport(c)
	})
	s.bot.Handle(telebot.OnVoice, func(c telebot.Context) error {
		return s.handlePrivateMediaImport(c)
	})
	s.bot.Handle(telebot.OnAudio, func(c telebot.Context) error {
		return s.handlePrivateMediaImport(c)
	})

	s.bot.Handle(telebot.OnCallback, func(c telebot.Context) error {
		callback := c.Callback()
		data, ok := extractMediaCallbackData(callback)
		if !ok {
			return nil
		}

		if !s.isPrivateChat(c.Chat()) {
			return nil
		}

		if err := c.Respond(); err != nil {
			s.logger.Debug("failed to respond to callback", "error", err)
		}

		return s.handleMediaCallback(c, data)
	})
}

func (s *Service) handlePrivateChatMessage(c telebot.Context) error {
	message := c.Message()
	if message == nil || message.Sender == nil || message.Sender.IsBot {
		return nil
	}

	if s.isAwaitingPackImport(message.Sender.ID) {
		pending, handled, err := s.buildPendingPackImportFromText(strings.TrimSpace(message.Text))
		if err != nil {
			return c.Send("Не удалось импортировать стикерпак: " + err.Error())
		}
		if handled {
			return s.saveImportedCollection(c, message.Sender.ID, pending)
		}
	}

	switch strings.TrimSpace(message.Text) {
	case "/media", "Медиатека", "медиатека":
		return s.sendMediaCabinetHome(c, "")
	}

	if s.isAwaitingPackImport(message.Sender.ID) {
		return c.Send("Сейчас жду ссылку на стикерпак или любой стикер из нужного набора.\n\nПример ссылки: https://t.me/addstickers/PackName")
	}

	return nil
}

func (s *Service) handlePrivateMediaImport(c telebot.Context) error {
	if !s.isPrivateChat(c.Chat()) {
		return nil
	}

	pending, err := s.buildPendingImport(c.Message())
	if err != nil {
		return c.Send("Не удалось импортировать это медиа: " + err.Error())
	}
	if pending == nil {
		if s.isAwaitingPackImport(c.Sender().ID) {
			return c.Send("Сейчас жду любой стикер из нужного набора или ссылку на стикерпак.")
		}
		return c.Send("Отправь стикер, gif/animation, voice или audio в личный чат с ботом.")
	}

	return s.saveImportedCollection(c, c.Sender().ID, pending)
}

func (s *Service) handleMediaCallback(c telebot.Context, data string) error {
	user := c.Sender()
	if user == nil {
		return nil
	}

	switch {
	case data == "home":
		s.clearAwaitingPackImport(user.ID)
		return s.sendMediaCabinetHome(c, "")
	case data == "packs":
		return s.sendMediaCollections(c, "")
	case data == "clear":
		s.clearPendingImport(user.ID)
		s.clearAwaitingPackImport(user.ID)
		return s.sendMediaCabinetHome(c, "Импорт очищен.")
	case data == "importpack":
		s.setAwaitingPackImport(user.ID)
		return c.Send(
			"Импорт стикерпака\n\nНажми на любой свой стикерпак в Telegram и пришли сюда любой стикер из него.\n" +
				"Либо пришли ссылку вида https://t.me/addstickers/PackName.\n\n" +
				"После этого я импортирую весь набор и предложу сохранить его в медиатеку.",
			s.buildAwaitingPackMarkup(),
		)
	case data == "importnew":
		return s.handleImportToNewCollection(c, user.ID)
	case strings.HasPrefix(data, "importto:"):
		collectionID, err := parseInt64(strings.TrimPrefix(data, "importto:"))
		if err != nil {
			return c.Send("Не удалось определить коллекцию.")
		}
		return s.handleImportToExistingCollection(c, user.ID, collectionID)
	case strings.HasPrefix(data, "toggle:"):
		collectionID, err := parseInt64(strings.TrimPrefix(data, "toggle:"))
		if err != nil {
			return c.Send("Не удалось определить пак.")
		}
		return s.toggleMediaCollection(c, user.ID, collectionID)
	case strings.HasPrefix(data, "delete:"):
		collectionID, err := parseInt64(strings.TrimPrefix(data, "delete:"))
		if err != nil {
			return c.Send("Не удалось определить пак.")
		}
		return s.deleteMediaCollection(c, user.ID, collectionID)
	default:
		return nil
	}
}

func (s *Service) sendMediaCabinetHome(c telebot.Context, prefix string) error {
	text := "Медиатека бота\n\n" +
		"Нажми кнопку \"Импортировать стикерпак\", если хочешь нативно добавить любой свой набор.\n\n" +
		"Также сюда можно прислать:\n" +
		"- стикер из sticker pack, чтобы импортировать весь пак;\n" +
		"- gif/animation;\n" +
		"- voice;\n" +
		"- audio.\n\n" +
		"После импорта бот сразу сохранит медиа в БД как новый пак и покажет обновленный список."

	if prefix != "" {
		text = prefix + "\n\n" + text
	}

	return c.Send(text, s.buildMediaHomeMarkup())
}

func (s *Service) sendMediaCollections(c telebot.Context, prefix string) error {
	user := c.Sender()
	if user == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collections, err := s.store.ListMediaCollectionsByOwner(ctx, user.ID)
	if err != nil {
		s.logger.Error("failed to list media collections", "telegram_id", user.ID, "error", err)
		return c.Send("Не удалось загрузить сохраненные паки.")
	}

	var builder strings.Builder
	if prefix != "" {
		builder.WriteString(prefix)
		builder.WriteString("\n\n")
	}
	builder.WriteString("Сохраненные паки:\n")
	if len(collections) == 0 {
		builder.WriteString("Пока пусто.")
		if s.getPendingImport(user.ID) != nil {
			builder.WriteString("\n\nТекущий импорт еще не сохранен. Нажми \"Сохранить текущий импорт\", чтобы создать первый пак.")
			return c.Send(builder.String(), s.buildCollectionsMarkup(collections, user.ID))
		}
		builder.WriteString(" Пришли медиа в личку, чтобы создать первый пак.")
		return c.Send(builder.String(), s.buildMediaHomeMarkup())
	}

	for _, collection := range collections {
		state := "выключен"
		if collection.Enabled {
			state = "включен"
		}
		builder.WriteString(fmt.Sprintf("• %s [%s] (%d)\n", collection.Name, state, collection.ItemCount))
	}

	return c.Send(builder.String(), s.buildCollectionsMarkup(collections, user.ID))
}

func (s *Service) buildPendingImport(message *telebot.Message) (*pendingMediaImport, error) {
	if message == nil {
		return nil, nil
	}

	if sticker := message.Sticker; sticker != nil {
		if strings.TrimSpace(sticker.SetName) != "" {
			return s.buildStickerPackImport(sticker.SetName)
		}

		return &pendingMediaImport{
			Summary:       "Подготовлен одиночный стикер для импорта.",
			SuggestedName: fmt.Sprintf("Sticker import %s", time.Now().Format("2006-01-02 15:04")),
			SourceType:    "telegram_sticker",
			SourceRef:     sticker.File.FileID,
			Tags:          defaultTagsForKind(mediafeature.KindSticker),
			Items: []storage.MediaItem{{
				Kind:   string(mediafeature.KindSticker),
				Source: sticker.File.FileID,
				Tags:   joinTags(defaultTagsForKind(mediafeature.KindSticker)),
				Weight: 1,
			}},
		}, nil
	}

	if animation := message.Animation; animation != nil {
		return &pendingMediaImport{
			Summary:       "Подготовлена animation/gif для импорта.",
			SuggestedName: fmt.Sprintf("GIFs %s", time.Now().Format("2006-01-02")),
			SourceType:    "telegram_animation",
			SourceRef:     animation.File.FileID,
			Tags:          defaultTagsForKind(mediafeature.KindAnimation),
			Items: []storage.MediaItem{{
				Kind:   string(mediafeature.KindAnimation),
				Source: animation.File.FileID,
				Tags:   joinTags(defaultTagsForKind(mediafeature.KindAnimation)),
				Weight: 1,
			}},
		}, nil
	}

	if voice := message.Voice; voice != nil {
		return &pendingMediaImport{
			Summary:       "Подготовлена voice-запись для импорта.",
			SuggestedName: fmt.Sprintf("Sounds %s", time.Now().Format("2006-01-02")),
			SourceType:    "telegram_voice",
			SourceRef:     voice.File.FileID,
			Tags:          defaultTagsForKind(mediafeature.KindVoice),
			Items: []storage.MediaItem{{
				Kind:   string(mediafeature.KindVoice),
				Source: voice.File.FileID,
				Tags:   joinTags(defaultTagsForKind(mediafeature.KindVoice)),
				Weight: 1,
			}},
		}, nil
	}

	if audio := message.Audio; audio != nil {
		return &pendingMediaImport{
			Summary:       "Подготовлен audio-файл для импорта.",
			SuggestedName: fmt.Sprintf("Sounds %s", time.Now().Format("2006-01-02")),
			SourceType:    "telegram_audio",
			SourceRef:     audio.File.FileID,
			Tags:          defaultTagsForKind(mediafeature.KindAudio),
			Items: []storage.MediaItem{{
				Kind:   string(mediafeature.KindAudio),
				Source: audio.File.FileID,
				Tags:   joinTags(defaultTagsForKind(mediafeature.KindAudio)),
				Weight: 1,
			}},
		}, nil
	}

	return nil, nil
}

func (s *Service) buildStickerPackImport(setName string) (*pendingMediaImport, error) {
	stickerSet, err := s.fetchStickerSet(setName)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(stickerSet.Title)
	if name == "" {
		name = setName
	}
	tags := inferImportTags(name, "telegram_sticker_set", mediafeature.KindSticker)

	items := make([]storage.MediaItem, 0, len(stickerSet.Stickers))
	for _, sticker := range stickerSet.Stickers {
		if strings.TrimSpace(sticker.FileID) == "" {
			continue
		}
		items = append(items, storage.MediaItem{
			Kind:   string(mediafeature.KindSticker),
			Source: sticker.FileID,
			Tags:   joinTags(tags),
			Weight: 1,
		})
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("в стикерпаке не найдено доступных стикеров")
	}

	return &pendingMediaImport{
		Summary:       buildImportSummary(name, len(items), tags),
		SuggestedName: name,
		SourceType:    "telegram_sticker_set",
		SourceRef:     stickerSet.Name,
		Tags:          tags,
		Items:         items,
	}, nil
}

func (s *Service) fetchStickerSet(name string) (*telegramStickerSet, error) {
	data, err := s.bot.Raw("getStickerSet", map[string]string{"name": name})
	if err != nil {
		return nil, fmt.Errorf("get sticker set: %w", err)
	}

	var response struct {
		Result telegramStickerSet `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode sticker set: %w", err)
	}

	return &response.Result, nil
}

func (s *Service) handleImportToNewCollection(c telebot.Context, userID int64) error {
	pending := s.getPendingImport(userID)
	if pending == nil {
		return c.Send("Нет активного импорта. Пришли медиа в личный чат с ботом.")
	}

	return s.saveImportedCollection(c, userID, pending)
}

func (s *Service) saveImportedCollection(c telebot.Context, userID int64, pending *pendingMediaImport) error {
	if pending == nil {
		return c.Send("Нет активного импорта. Пришли медиа в личный чат с ботом.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	existing, err := s.store.GetMediaCollectionBySource(ctx, userID, pending.SourceType, pending.SourceRef)
	if err != nil {
		s.logger.Error("failed to check media collection duplicate", "telegram_id", userID, "source_type", pending.SourceType, "source_ref", pending.SourceRef, "error", err)
		return c.Send("Не удалось проверить существующие паки.")
	}
	if existing != nil {
		if err := s.store.AddMediaItems(ctx, existing.ID, pending.Items); err != nil {
			s.logger.Error("failed to sync duplicate media collection items", "collection_id", existing.ID, "error", err)
			return c.Send("Найден совпадающий пак, но не удалось обновить его содержимое.")
		}

		s.clearPendingImport(userID)
		s.clearAwaitingPackImport(userID)
		s.reloadMediaLibrary()

		return s.sendMediaCollections(c, fmt.Sprintf("%s\n\nПак \"%s\" уже существует. Дубликат не создан, содержимое синхронизировано.", pending.Summary, existing.Name))
	}

	collection, err := s.store.CreateMediaCollection(ctx, userID, pending.SuggestedName, pending.SourceType, pending.SourceRef, true)
	if err != nil {
		s.logger.Error("failed to create media collection", "telegram_id", userID, "error", err)
		return c.Send("Не удалось создать новый пак.")
	}
	if err := s.store.AddMediaItems(ctx, collection.ID, pending.Items); err != nil {
		s.logger.Error("failed to add imported media items", "collection_id", collection.ID, "error", err)
		return c.Send("Не удалось сохранить элементы в новый пак.")
	}

	s.clearPendingImport(userID)
	s.clearAwaitingPackImport(userID)
	s.reloadMediaLibrary()

	return s.sendMediaCollections(c, fmt.Sprintf("%s\n\nПак \"%s\" сохранен в БД, импортировано %d элементов.", pending.Summary, collection.Name, len(pending.Items)))
}

func (s *Service) handleImportToExistingCollection(c telebot.Context, userID, collectionID int64) error {
	pending := s.getPendingImport(userID)
	if pending == nil {
		return c.Send("Нет активного импорта. Пришли медиа в личный чат с ботом.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collections, err := s.store.ListMediaCollectionsByOwner(ctx, userID)
	if err != nil {
		s.logger.Error("failed to list media collections for import", "telegram_id", userID, "error", err)
		return c.Send("Не удалось загрузить список паков.")
	}

	var target *storage.MediaCollection
	for i := range collections {
		if collections[i].ID == collectionID {
			target = &collections[i]
			break
		}
	}
	if target == nil {
		return c.Send("Выбранный пак не найден.")
	}

	if err := s.store.AddMediaItems(ctx, collectionID, pending.Items); err != nil {
		s.logger.Error("failed to add media items to existing collection", "collection_id", collectionID, "error", err)
		return c.Send("Не удалось добавить элементы в выбранный пак.")
	}

	s.clearPendingImport(userID)
	s.clearAwaitingPackImport(userID)
	s.reloadMediaLibrary()

	return s.sendMediaCollections(c, fmt.Sprintf("Импортировано %d элементов в пак \"%s\".", len(pending.Items), target.Name))
}

func (s *Service) deleteMediaCollection(c telebot.Context, userID, collectionID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collections, err := s.store.ListMediaCollectionsByOwner(ctx, userID)
	if err != nil {
		return c.Send("Не удалось загрузить паки.")
	}

	var target *storage.MediaCollection
	for i := range collections {
		if collections[i].ID == collectionID {
			target = &collections[i]
			break
		}
	}
	if target == nil {
		return c.Send("Пак не найден.")
	}

	if err := s.store.DeleteMediaCollection(ctx, collectionID, userID); err != nil {
		s.logger.Error("failed to delete media collection", "collection_id", collectionID, "telegram_id", userID, "error", err)
		return c.Send("Не удалось удалить пак.")
	}

	s.reloadMediaLibrary()
	return s.sendMediaCollections(c, fmt.Sprintf("Пак \"%s\" удален.", target.Name))
}

func (s *Service) toggleMediaCollection(c telebot.Context, userID, collectionID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collections, err := s.store.ListMediaCollectionsByOwner(ctx, userID)
	if err != nil {
		return c.Send("Не удалось загрузить паки.")
	}

	var target *storage.MediaCollection
	for i := range collections {
		if collections[i].ID == collectionID {
			target = &collections[i]
			break
		}
	}
	if target == nil {
		return c.Send("Пак не найден.")
	}

	if err := s.store.SetMediaCollectionEnabled(ctx, collectionID, userID, !target.Enabled); err != nil {
		s.logger.Error("failed to toggle media collection", "collection_id", collectionID, "telegram_id", userID, "error", err)
		return c.Send("Не удалось переключить пак.")
	}

	s.reloadMediaLibrary()
	state := "выключен"
	if !target.Enabled {
		state = "включен"
	}
	return s.sendMediaCollections(c, fmt.Sprintf("Пак \"%s\" теперь %s.", target.Name, state))
}

func (s *Service) reloadMediaLibrary() {
	if s.media == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	items, err := s.store.ListEnabledMediaItems(ctx)
	if err != nil {
		s.logger.Error("failed to reload media library", "error", err)
		return
	}

	library := make([]mediafeature.Item, 0, len(items))
	for _, item := range items {
		library = append(library, mediafeature.Item{
			Kind:    mediafeature.Kind(item.Kind),
			Source:  item.Source,
			Caption: item.Caption,
			Tags:    splitTags(item.Tags),
			Group:   strings.TrimSpace(item.CollectionName),
			Weight:  item.Weight,
		})
	}

	s.media.ReplaceLibrary(library)
	s.logger.Info("media library reloaded", "enabled_item_count", len(library))
}

func (s *Service) buildMediaHomeMarkup() *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	importPack := menu.Data("Импортировать стикерпак", mediaCallbackUnique, "importpack")
	home := menu.Data("Сохраненные паки", mediaCallbackUnique, "packs")
	clear := menu.Data("Сбросить импорт", mediaCallbackUnique, "clear")
	menu.Inline(
		menu.Row(importPack),
		menu.Row(home),
		menu.Row(clear),
	)
	return menu
}

func (s *Service) buildAwaitingPackMarkup() *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	back := menu.Data("Назад", mediaCallbackUnique, "home")
	cancel := menu.Data("Отмена импорта", mediaCallbackUnique, "clear")
	menu.Inline(
		menu.Row(back),
		menu.Row(cancel),
	)
	return menu
}

func (s *Service) buildImportTargetMarkup(userID int64) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	createNew := menu.Data("В новый пак", mediaCallbackUnique, "importnew")
	showPacks := menu.Data("Список паков", mediaCallbackUnique, "packs")
	clear := menu.Data("Отмена", mediaCallbackUnique, "clear")

	rows := []telebot.Row{
		menu.Row(createNew),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collections, err := s.store.ListMediaCollectionsByOwner(ctx, userID)
	if err == nil {
		for _, collection := range collections {
			rows = append(rows, menu.Row(menu.Data(trimInlineLabel("В "+collection.Name), mediaCallbackUnique, fmt.Sprintf("importto:%d", collection.ID))))
		}
	}

	rows = append(rows, menu.Row(showPacks), menu.Row(clear))
	menu.Inline(rows...)
	return menu
}

func (s *Service) buildCollectionsMarkup(collections []storage.MediaCollection, userID int64) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	rows := make([]telebot.Row, 0, len(collections)+2)
	for _, collection := range collections {
		label := "Включить"
		if collection.Enabled {
			label = "Выключить"
		}
		toggleBtn := menu.Data(trimInlineLabel(label+": "+collection.Name), mediaCallbackUnique, fmt.Sprintf("toggle:%d", collection.ID))
		deleteBtn := menu.Data("Удалить", mediaCallbackUnique, fmt.Sprintf("delete:%d", collection.ID))
		rows = append(rows, menu.Row(toggleBtn, deleteBtn))
	}

	if s.getPendingImport(userID) != nil {
		rows = append(rows, menu.Row(menu.Data("Сохранить текущий импорт", mediaCallbackUnique, "importnew")))
	}
	rows = append(rows, menu.Row(menu.Data("Импортировать стикерпак", mediaCallbackUnique, "importpack")))
	rows = append(rows, menu.Row(menu.Data("Назад", mediaCallbackUnique, "home")))
	menu.Inline(rows...)
	return menu
}

func (s *Service) setPendingImport(userID int64, pending *pendingMediaImport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[userID] = pending
}

func (s *Service) getPendingImport(userID int64) *pendingMediaImport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending[userID]
}

func (s *Service) clearPendingImport(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, userID)
}

func (s *Service) setAwaitingPackImport(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.awaitingPackImport[userID] = true
}

func (s *Service) isAwaitingPackImport(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.awaitingPackImport[userID]
}

func (s *Service) clearAwaitingPackImport(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.awaitingPackImport, userID)
}

func trimInlineLabel(label string) string {
	const maxLen = 56
	if len(label) <= maxLen {
		return label
	}
	return label[:maxLen]
}

func parseInt64(raw string) (int64, error) {
	var value int64
	_, err := fmt.Sscanf(raw, "%d", &value)
	if err != nil {
		return 0, err
	}
	return value, nil
}

type telegramStickerSet struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Stickers []struct {
		FileID string `json:"file_id"`
	} `json:"stickers"`
}

func (s *Service) buildPendingPackImportFromText(text string) (*pendingMediaImport, bool, error) {
	setName := extractStickerSetName(text)
	if setName == "" {
		return nil, false, nil
	}

	pending, err := s.buildStickerPackImport(setName)
	if err != nil {
		return nil, true, err
	}

	return pending, true, nil
}

func extractStickerSetName(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	text = strings.TrimPrefix(text, "https://")
	text = strings.TrimPrefix(text, "http://")
	text = strings.TrimPrefix(text, "t.me/")
	text = strings.TrimPrefix(text, "telegram.me/")

	if strings.HasPrefix(text, "addstickers/") {
		text = strings.TrimPrefix(text, "addstickers/")
	}

	text = strings.Trim(text, "/")
	if idx := strings.IndexAny(text, " ?#"); idx >= 0 {
		text = text[:idx]
	}

	if strings.Contains(text, "/") {
		return ""
	}

	return strings.TrimSpace(text)
}

func extractMediaCallbackData(callback *telebot.Callback) (string, bool) {
	if callback == nil {
		return "", false
	}

	if callback.Unique == mediaCallbackUnique && strings.TrimSpace(callback.Data) != "" {
		return strings.TrimSpace(callback.Data), true
	}

	raw := callback.Data
	if raw == "" || !strings.HasPrefix(raw, mediaCallbackPrefix) {
		return "", false
	}

	return strings.TrimSpace(strings.TrimPrefix(raw, mediaCallbackPrefix)), true
}

func buildImportSummary(name string, itemCount int, tags []string) string {
	summary := fmt.Sprintf("Подготовлен импорт sticker pack \"%s\" (%d стикеров).", name, itemCount)
	if len(tags) == 0 {
		return summary
	}

	return fmt.Sprintf("%s Теги: %s.", summary, strings.Join(tags, ", "))
}

func inferImportTags(name, sourceType string, kind mediafeature.Kind) []string {
	candidate := normalizeImportLabel(name + " " + sourceType)
	tags := defaultTagsForKind(kind)

	if strings.Contains(candidate, "ахах") || strings.Contains(candidate, "хаха") || strings.Contains(candidate, "лол") || strings.Contains(candidate, "ору") || strings.Contains(candidate, "ржу") {
		tags = append(tags, "laugh")
	}
	if strings.Contains(candidate, "жесть") || strings.Contains(candidate, "шок") || strings.Contains(candidate, "имба") || strings.Contains(candidate, "разнос") {
		tags = append(tags, "hype")
	}
	if strings.Contains(candidate, "кринж") || strings.Contains(candidate, "facepalm") {
		tags = append(tags, "cringe")
	}
	if strings.Contains(candidate, "кот") || strings.Contains(candidate, "cat") || strings.Contains(candidate, "dog") || strings.Contains(candidate, "собак") {
		tags = append(tags, "animal")
	}
	if strings.Contains(candidate, "sound") || strings.Contains(candidate, "саунд") || strings.Contains(candidate, "voice") || strings.Contains(candidate, "голос") {
		tags = append(tags, "sound")
	}

	return uniqueTags(tags)
}

func defaultTagsForKind(kind mediafeature.Kind) []string {
	switch kind {
	case mediafeature.KindSticker:
		return []string{"meme", "reaction", "sticker"}
	case mediafeature.KindAnimation:
		return []string{"meme", "reaction", "animation"}
	case mediafeature.KindVoice:
		return []string{"sound", "voice", "reaction"}
	case mediafeature.KindAudio:
		return []string{"sound", "audio", "reaction"}
	default:
		return []string{"reaction"}
	}
}

func normalizeImportLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		"ё", "е",
		"_", " ",
		"-", " ",
	)
	return replacer.Replace(value)
}

func splitTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}

	return uniqueTags(result)
}

func joinTags(tags []string) string {
	return strings.Join(uniqueTags(tags), ",")
}

func uniqueTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.ToLower(tag))
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}

	return result
}
