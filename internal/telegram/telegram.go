package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	telebot "gopkg.in/telebot.v3"

	"tgbotforourgroup/internal/storage"
)

type Store interface {
	UpsertMapping(ctx context.Context, mapping storage.UserMapping) error
	GetMappingsByTelegramID(ctx context.Context, telegramID int64) ([]storage.UserMapping, error)
	DeleteMappingsByTelegramUser(ctx context.Context, telegramChatID, telegramID int64) (int64, error)
	GetInviteToken(ctx context.Context, token string) (*storage.InviteToken, error)
	ConsumeInviteToken(ctx context.Context, token string) (bool, error)
}

type Service struct {
	bot         *telebot.Bot
	targetChats map[int64]*telebot.Chat
	chatLabels  map[int64]string
	store       Store
	logger      *slog.Logger
	mu          sync.Mutex
	statuses    map[string]*voiceStatusMessage
}

type voiceStatusMessage struct {
	message *telebot.Message
	text    string
}

type linkedChat struct {
	ChatID int64
	Label  string
}

func NewService(token string, targetChatIDs []int64, store Store, logger *slog.Logger) (*Service, error) {
	bot, err := telebot.NewBot(telebot.Settings{
		Token: token,
		Poller: &telebot.LongPoller{
			Timeout: 10 * time.Second,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	service := &Service{
		bot:         bot,
		targetChats: make(map[int64]*telebot.Chat),
		chatLabels:  make(map[int64]string),
		store:       store,
		logger:      logger.With("component", "telegram"),
		statuses:    make(map[string]*voiceStatusMessage),
	}
	service.initTargetChats(targetChatIDs)
	service.registerHandlers()

	return service, nil
}

func (s *Service) Start() {
	s.logger.Info("telegram bot polling started")
	s.bot.Start()
	s.logger.Info("telegram bot polling stopped")
}

func (s *Service) Stop() {
	s.bot.Stop()
}

func (s *Service) UpsertVoiceChannelStatus(chatID int64, channelID, text string) error {
	targetChat, ok := s.targetChats[chatID]
	if !ok {
		return fmt.Errorf("telegram chat %d is not configured", chatID)
	}
	// #region debug-point G:telegram-status-upsert
	s.logger.Info("[DEBUG] upserting telegram voice status", "hypothesis_id", "G", "telegram_chat_id", chatID, "channel_id", channelID)
	// #endregion

	statusKey := voiceStatusKey(chatID, channelID)

	s.mu.Lock()
	status := s.statuses[statusKey]
	s.mu.Unlock()

	if status != nil && status.text == text {
		return nil
	}

	if status == nil || status.message == nil {
		message, err := s.bot.Send(targetChat, text)
		if err != nil {
			return fmt.Errorf("send telegram voice status: %w", err)
		}
		// #region debug-point G:telegram-status-created
		s.logger.Info("[DEBUG] telegram voice status created", "hypothesis_id", "G", "telegram_chat_id", chatID, "channel_id", channelID, "message_id", message.ID)
		// #endregion

		s.mu.Lock()
		s.statuses[statusKey] = &voiceStatusMessage{
			message: message,
			text:    text,
		}
		s.mu.Unlock()

		return nil
	}

	editedMessage, err := s.bot.Edit(status.message, text)
	if err != nil {
		return fmt.Errorf("edit telegram voice status: %w", err)
	}
	if editedMessage == nil {
		editedMessage = status.message
	}
	// #region debug-point G:telegram-status-edited
	s.logger.Info("[DEBUG] telegram voice status edited", "hypothesis_id", "G", "telegram_chat_id", chatID, "channel_id", channelID, "message_id", editedMessage.ID)
	// #endregion

	s.mu.Lock()
	s.statuses[statusKey] = &voiceStatusMessage{
		message: editedMessage,
		text:    text,
	}
	s.mu.Unlock()

	return nil
}

func (s *Service) CloseVoiceChannelStatus(chatID int64, channelID, text string) error {
	statusKey := voiceStatusKey(chatID, channelID)

	s.mu.Lock()
	status := s.statuses[statusKey]
	s.mu.Unlock()

	if status == nil || status.message == nil {
		return nil
	}

	if status.text != text {
		editedMessage, err := s.bot.Edit(status.message, text)
		if err != nil {
			return fmt.Errorf("edit final telegram voice status: %w", err)
		}
		if editedMessage != nil {
			status.message = editedMessage
		}
	}

	s.mu.Lock()
	delete(s.statuses, statusKey)
	s.mu.Unlock()

	return nil
}

func (s *Service) ChatLabel(chatID int64) string {
	if label, ok := s.chatLabels[chatID]; ok && strings.TrimSpace(label) != "" {
		return label
	}

	return fmt.Sprintf("Telegram chat %d", chatID)
}

func (s *Service) registerHandlers() {
	s.bot.Handle("/start", func(c telebot.Context) error {
		message := c.Message()
		if message == nil {
			return nil
		}

		payload := strings.TrimSpace(message.Payload)
		telegramUser := c.Sender()
		if payload == "" {
			return s.sendStartScreen(c, telegramUser, "")
		}

		if strings.HasPrefix(payload, "unlink_") {
			if telegramUser == nil {
				return c.Send("Не удалось определить пользователя Telegram. Попробуй еще раз.")
			}

			chatID, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(payload, "unlink_")), 10, 64)
			if err != nil {
				return c.Send("Не удалось определить беседу для отвязки. Попробуй еще раз.")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			deleted, err := s.store.DeleteMappingsByTelegramUser(ctx, chatID, telegramUser.ID)
			if err != nil {
				s.logger.Error("failed to delete telegram-discord mapping", "telegram_chat_id", chatID, "telegram_id", telegramUser.ID, "error", err)
				return c.Send("Не удалось удалить привязку. Попробуй еще раз чуть позже.")
			}
			if deleted == 0 {
				return s.sendStartScreen(c, telegramUser, "Активная привязка для этой беседы не найдена.")
			}

			s.logger.Info("telegram-discord mappings deleted", "telegram_chat_id", chatID, "telegram_id", telegramUser.ID, "deleted_rows", deleted)

			return s.sendStartScreen(c, telegramUser, fmt.Sprintf("Готово! Привязка к беседе \"%s\" удалена.", s.ChatLabel(chatID)))
		}

		if !strings.HasPrefix(payload, "auth_") {
			return c.Send("Не удалось обработать параметр привязки. Запусти ссылку из Discord еще раз.")
		}

		tokenValue := strings.TrimSpace(strings.TrimPrefix(payload, "auth_"))
		if tokenValue == "" {
			return c.Send("Не удалось определить токен привязки. Запусти ссылку из Discord еще раз.")
		}

		if telegramUser == nil {
			return c.Send("Не удалось определить пользователя Telegram. Попробуй еще раз.")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		inviteToken, err := s.store.GetInviteToken(ctx, tokenValue)
		if err != nil {
			s.logger.Error("failed to load invite token", "token", tokenValue, "telegram_id", telegramUser.ID, "error", err)
			return c.Send("Не удалось проверить ссылку привязки. Попробуй еще раз чуть позже.")
		}
		if inviteToken == nil {
			return c.Send("Ссылка привязки недействительна или уже устарела.")
		}
		if inviteToken.UsedAt != nil {
			return c.Send("Эта ссылка привязки уже была использована.")
		}
		if time.Now().After(inviteToken.ExpiresAt) {
			return c.Send("Срок действия ссылки привязки истек. Попроси новую ссылку.")
		}

		isMember, err := s.isUserInTargetChat(inviteToken.TelegramChatID, telegramUser)
		if err != nil {
			s.logger.Error("failed to validate telegram chat membership", "telegram_id", telegramUser.ID, "target_chat_id", inviteToken.TelegramChatID, "error", err)
			return c.Send("Не удалось проверить участие в целевой беседе. Попробуй еще раз чуть позже.")
		}
		if !isMember {
			s.logger.Warn("telegram user is not a member of target chat", "telegram_id", telegramUser.ID, "target_chat_id", inviteToken.TelegramChatID)
			return c.Send("Привязка недоступна: тебя нет в целевой беседе Telegram.")
		}

		consumed, err := s.store.ConsumeInviteToken(ctx, tokenValue)
		if err != nil {
			s.logger.Error("failed to consume invite token", "token", tokenValue, "telegram_id", telegramUser.ID, "error", err)
			return c.Send("Не удалось зафиксировать использование ссылки. Попробуй запросить новую.")
		}
		if !consumed {
			return c.Send("Эта ссылка привязки уже была использована или устарела.")
		}

		mapping := storage.UserMapping{
			DiscordID:      inviteToken.DiscordID,
			TelegramChatID: inviteToken.TelegramChatID,
			TelegramID:     telegramUser.ID,
			TelegramName:   buildTelegramName(telegramUser),
		}

		if err := s.store.UpsertMapping(ctx, mapping); err != nil {
			s.logger.Error("failed to save telegram-discord mapping", "discord_id", inviteToken.DiscordID, "telegram_chat_id", inviteToken.TelegramChatID, "telegram_id", telegramUser.ID, "error", err)
			return c.Send("Не удалось сохранить привязку. Попробуй еще раз чуть позже.")
		}

		s.logger.Info("telegram-discord mapping saved", "discord_id", inviteToken.DiscordID, "telegram_chat_id", inviteToken.TelegramChatID, "telegram_id", telegramUser.ID, "telegram_name", mapping.TelegramName)

		return s.sendStartScreen(c, telegramUser, fmt.Sprintf("Успешно! Твой аккаунт связан с беседой \"%s\".", s.ChatLabel(inviteToken.TelegramChatID)))
	})
}

func (s *Service) initTargetChats(targetChatIDs []int64) {
	for _, chatID := range targetChatIDs {
		chat := &telebot.Chat{ID: chatID}
		s.targetChats[chatID] = chat
		s.chatLabels[chatID] = fmt.Sprintf("%d", chatID)

		resolvedChat, err := s.bot.ChatByID(chatID)
		if err != nil || resolvedChat == nil {
			s.logger.Warn("failed to resolve telegram chat info", "telegram_chat_id", chatID, "error", err)
			continue
		}

		s.targetChats[chatID] = resolvedChat
		s.chatLabels[chatID] = chatLabel(resolvedChat)
	}
}

func (s *Service) isUserInTargetChat(chatID int64, user *telebot.User) (bool, error) {
	if user == nil {
		return false, nil
	}

	targetChat, ok := s.targetChats[chatID]
	if !ok {
		return false, fmt.Errorf("telegram chat %d is not configured", chatID)
	}

	member, err := s.bot.ChatMemberOf(targetChat, user)
	if err != nil {
		return false, fmt.Errorf("get telegram chat member: %w", err)
	}
	if member == nil {
		return false, nil
	}

	switch member.Role {
	case telebot.Creator, telebot.Administrator, telebot.Member, telebot.Restricted:
		return true, nil
	default:
		return false, nil
	}
}

func voiceStatusKey(chatID int64, channelID string) string {
	return fmt.Sprintf("%d:%s", chatID, channelID)
}

func chatLabel(chat *telebot.Chat) string {
	if chat == nil {
		return ""
	}

	if strings.TrimSpace(chat.Title) != "" {
		return chat.Title
	}
	if strings.TrimSpace(chat.Username) != "" {
		return "@" + chat.Username
	}

	return fmt.Sprintf("%d", chat.ID)
}

func buildTelegramName(user *telebot.User) string {
	if user == nil {
		return "Telegram user"
	}

	fullName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if fullName != "" {
		return fullName
	}

	if user.Username != "" {
		return "@" + user.Username
	}

	return fmt.Sprintf("Telegram user %d", user.ID)
}

func (s *Service) sendStartScreen(c telebot.Context, user *telebot.User, prefix string) error {
	if user == nil {
		return c.Send("Не удалось определить пользователя Telegram. Попробуй еще раз.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	linkedChats, err := s.linkedChatsByTelegramUser(ctx, user.ID)
	if err != nil {
		s.logger.Error("failed to load telegram-discord mappings for start screen", "telegram_id", user.ID, "error", err)
		if prefix != "" {
			return c.Send(prefix + "\n\nНе удалось загрузить текущие привязки. Попробуй еще раз чуть позже.")
		}

		return c.Send("Не удалось загрузить текущие привязки. Попробуй еще раз чуть позже.")
	}

	var text strings.Builder
	if prefix != "" {
		text.WriteString(prefix)
		text.WriteString("\n\n")
	}

	if len(linkedChats) == 0 {
		text.WriteString("Сейчас у тебя нет активных привязок. Открой Deep Link из Discord, чтобы связать аккаунт.")
		return c.Send(text.String())
	}

	text.WriteString("Текущие привязки:\n")
	for _, chat := range linkedChats {
		text.WriteString("• ")
		text.WriteString(chat.Label)
		text.WriteString("\n")
	}
	text.WriteString("\nНажми на кнопку ниже, чтобы отвязать себя от нужной беседы.")

	return c.Send(text.String(), s.buildUnlinkMarkup(linkedChats))
}

func (s *Service) linkedChatsByTelegramUser(ctx context.Context, telegramID int64) ([]linkedChat, error) {
	mappings, err := s.store.GetMappingsByTelegramID(ctx, telegramID)
	if err != nil {
		return nil, err
	}

	seen := make(map[int64]struct{}, len(mappings))
	linkedChats := make([]linkedChat, 0, len(mappings))
	for _, mapping := range mappings {
		if _, exists := seen[mapping.TelegramChatID]; exists {
			continue
		}

		seen[mapping.TelegramChatID] = struct{}{}
		linkedChats = append(linkedChats, linkedChat{
			ChatID: mapping.TelegramChatID,
			Label:  s.ChatLabel(mapping.TelegramChatID),
		})
	}

	sort.Slice(linkedChats, func(i, j int) bool {
		return linkedChats[i].ChatID < linkedChats[j].ChatID
	})

	return linkedChats, nil
}

func (s *Service) buildUnlinkMarkup(linkedChats []linkedChat) *telebot.ReplyMarkup {
	markup := &telebot.ReplyMarkup{}
	rows := make([][]telebot.InlineButton, 0, len(linkedChats))
	for _, chat := range linkedChats {
		rows = append(rows, []telebot.InlineButton{
			{
				Text: truncateTelegramButtonLabel("Отвязать: " + chat.Label),
				URL:  fmt.Sprintf("https://t.me/%s?start=unlink_%d", s.bot.Me.Username, chat.ChatID),
			},
		})
	}

	markup.InlineKeyboard = rows
	return markup
}

func truncateTelegramButtonLabel(label string) string {
	const maxLen = 64
	if len(label) <= maxLen {
		return label
	}

	return label[:maxLen]
}
