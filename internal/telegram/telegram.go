package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	telebot "gopkg.in/telebot.v3"

	"tgbotforourgroup/internal/storage"
)

type Store interface {
	UpsertMapping(ctx context.Context, mapping storage.UserMapping) error
}

type Service struct {
	bot        *telebot.Bot
	targetChat *telebot.Chat
	store      Store
	logger     *slog.Logger
}

func NewService(token string, targetChatID int64, store Store, logger *slog.Logger) (*Service, error) {
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
		bot:        bot,
		targetChat: &telebot.Chat{ID: targetChatID},
		store:      store,
		logger:     logger.With("component", "telegram"),
	}

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

func (s *Service) NotifyVoiceJoin(telegramName string) error {
	message := fmt.Sprintf("👤 %s зашел в голосовой канал Discord!", telegramName)
	_, err := s.bot.Send(s.targetChat, message)
	if err != nil {
		return fmt.Errorf("send telegram notification: %w", err)
	}

	return nil
}

func (s *Service) registerHandlers() {
	s.bot.Handle("/start", func(c telebot.Context) error {
		message := c.Message()
		if message == nil {
			return nil
		}

		payload := strings.TrimSpace(message.Payload)
		if payload == "" {
			return c.Send("Привет! Открой Deep Link из Discord, чтобы привязать аккаунт.")
		}

		if !strings.HasPrefix(payload, "auth_") {
			return c.Send("Не удалось обработать параметр привязки. Запусти ссылку из Discord еще раз.")
		}

		discordID := strings.TrimSpace(strings.TrimPrefix(payload, "auth_"))
		if discordID == "" {
			return c.Send("Не удалось определить Discord ID. Запусти ссылку из Discord еще раз.")
		}

		telegramUser := c.Sender()
		if telegramUser == nil {
			return c.Send("Не удалось определить пользователя Telegram. Попробуй еще раз.")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		mapping := storage.UserMapping{
			DiscordID:    discordID,
			TelegramID:   telegramUser.ID,
			TelegramName: buildTelegramName(telegramUser),
		}

		if err := s.store.UpsertMapping(ctx, mapping); err != nil {
			s.logger.Error("failed to save telegram-discord mapping", "discord_id", discordID, "telegram_id", telegramUser.ID, "error", err)
			return c.Send("Не удалось сохранить привязку. Попробуй еще раз чуть позже.")
		}

		s.logger.Info("telegram-discord mapping saved", "discord_id", discordID, "telegram_id", telegramUser.ID, "telegram_name", mapping.TelegramName)

		return c.Send("Успешно! Твои аккаунты связаны.")
	})
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
