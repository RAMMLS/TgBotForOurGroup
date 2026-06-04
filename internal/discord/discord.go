package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"tgbotforourgroup/internal/storage"
)

type Store interface {
	GetByDiscordID(ctx context.Context, discordID string) (*storage.UserMapping, error)
}

type Notifier interface {
	NotifyVoiceJoin(telegramName string) error
}

type Config struct {
	Token               string
	TargetGuildID       string
	TelegramBotUsername string
	DMCooldown          time.Duration
}

type Service struct {
	session             *discordgo.Session
	store               Store
	notifier            Notifier
	logger              *slog.Logger
	targetGuildID       string
	telegramBotUsername string
	dmCooldown          time.Duration

	mu          sync.Mutex
	lastDMAt    map[string]time.Time
	voiceStates map[string]string
}

func NewService(cfg Config, store Store, notifier Notifier, logger *slog.Logger) (*Service, error) {
	session, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("create discord session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuildMembers |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent |
		discordgo.IntentsGuildPresences

	service := &Service{
		session:             session,
		store:               store,
		notifier:            notifier,
		logger:              logger.With("component", "discord"),
		targetGuildID:       cfg.TargetGuildID,
		telegramBotUsername: cfg.TelegramBotUsername,
		dmCooldown:          cfg.DMCooldown,
		lastDMAt:            make(map[string]time.Time),
		voiceStates:         make(map[string]string),
	}

	session.AddHandler(service.onReady)
	session.AddHandler(service.onGuildCreate)
	session.AddHandler(service.onVoiceStateUpdate)

	return service, nil
}

func (s *Service) Start() error {
	if err := s.session.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}

	s.logger.Info("discord session opened")
	return nil
}

func (s *Service) Stop() error {
	if err := s.session.Close(); err != nil {
		return fmt.Errorf("close discord session: %w", err)
	}

	s.logger.Info("discord session closed")
	return nil
}

func (s *Service) onReady(_ *discordgo.Session, event *discordgo.Ready) {
	s.logger.Info("discord bot is ready", "user", event.User.String())
}

func (s *Service) onGuildCreate(_ *discordgo.Session, event *discordgo.GuildCreate) {
	if event.Guild == nil || event.Guild.ID != s.targetGuildID {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, voiceState := range event.Guild.VoiceStates {
		s.voiceStates[voiceState.UserID] = voiceState.ChannelID
	}

	s.logger.Info("discord voice state cache initialized", "guild_id", event.Guild.ID, "tracked_users", len(event.Guild.VoiceStates))
}

func (s *Service) onVoiceStateUpdate(session *discordgo.Session, event *discordgo.VoiceStateUpdate) {
	if event.GuildID != s.targetGuildID {
		return
	}

	userID := event.UserID
	if userID == "" {
		return
	}

	if session.State != nil && session.State.User != nil && userID == session.State.User.ID {
		return
	}

	previousChannelID := s.swapVoiceState(userID, event.ChannelID)
	if event.ChannelID == "" {
		return
	}

	if previousChannelID != "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mapping, err := s.store.GetByDiscordID(ctx, userID)
	if err != nil {
		s.logger.Error("failed to get user mapping", "discord_id", userID, "error", err)
		return
	}

	if mapping != nil {
		if err := s.notifier.NotifyVoiceJoin(mapping.TelegramName); err != nil {
			s.logger.Error("failed to notify telegram chat about voice join", "discord_id", userID, "telegram_name", mapping.TelegramName, "error", err)
			return
		}

		s.logger.Info("telegram chat notified about voice join", "discord_id", userID, "telegram_name", mapping.TelegramName, "channel_id", event.ChannelID)
		return
	}

	if !s.allowDM(userID) {
		s.logger.Debug("skipping discord dm because cooldown is active", "discord_id", userID)
		return
	}

	if err := s.sendDeepLinkDM(session, userID); err != nil {
		s.logger.Warn("failed to send discord dm with deep link", "discord_id", userID, "error", err)
		return
	}

	s.logger.Info("discord dm with deep link sent", "discord_id", userID)
}

func (s *Service) swapVoiceState(userID, newChannelID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	previousChannelID := s.voiceStates[userID]
	if newChannelID == "" {
		delete(s.voiceStates, userID)
	} else {
		s.voiceStates[userID] = newChannelID
	}

	return previousChannelID
}

func (s *Service) allowDM(userID string) bool {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	lastSentAt, exists := s.lastDMAt[userID]
	if exists && now.Sub(lastSentAt) < s.dmCooldown {
		return false
	}

	s.lastDMAt[userID] = now
	return true
}

func (s *Service) sendDeepLinkDM(session *discordgo.Session, userID string) error {
	channel, err := session.UserChannelCreate(userID)
	if err != nil {
		return fmt.Errorf("create dm channel: %w", err)
	}

	deepLink := fmt.Sprintf("https://t.me/%s?start=auth_%s", s.telegramBotUsername, userID)
	message := &discordgo.MessageSend{
		Content: "Привет! Чтобы группа в Telegram видела, когда ты заходишь в голосовые каналы, привяжи свой аккаунт, нажав на кнопку ниже.",
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label: "Привязать Telegram",
						Style: discordgo.LinkButton,
						URL:   deepLink,
					},
				},
			},
		},
	}

	if _, err := session.ChannelMessageSendComplex(channel.ID, message); err != nil {
		return fmt.Errorf("send deep link dm: %w", err)
	}

	return nil
}
