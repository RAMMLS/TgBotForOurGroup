package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"tgbotforourgroup/internal/storage"
)

type Store interface {
	GetMappingsByDiscordID(ctx context.Context, discordID string) ([]storage.UserMapping, error)
	CreateInviteToken(ctx context.Context, discordID string, telegramChatID, inviterTelegramID int64, ttl time.Duration) (*storage.InviteToken, error)
}

type Notifier interface {
	UpsertVoiceChannelStatus(chatID int64, channelID, text string) error
	CloseVoiceChannelStatus(chatID int64, channelID, text string) error
	ChatLabel(chatID int64) string
}

type Config struct {
	Token                string
	TargetGuildID        string
	TargetChatIDs        []int64
	TelegramBotUsername  string
	DMCooldown           time.Duration
	StatusUpdateInterval time.Duration
	InviteTokenTTL       time.Duration
}

type Service struct {
	session              *discordgo.Session
	store                Store
	notifier             Notifier
	logger               *slog.Logger
	targetGuildID        string
	targetChatIDs        []int64
	allowedChatIDs       map[int64]struct{}
	telegramBotUsername  string
	dmCooldown           time.Duration
	statusUpdateInterval time.Duration
	inviteTokenTTL       time.Duration

	mu               sync.Mutex
	lastDMAt         map[string]time.Time
	voiceStates      map[string]string
	voiceJoinedAt    map[string]time.Time
	activeSessions   map[string]activeVoiceSession
	cancelBackground context.CancelFunc
	backgroundWG     sync.WaitGroup
}

type activeVoiceSession struct {
	StartedAt     time.Time
	ActiveChatIDs map[int64]struct{}
}

type chatInvite struct {
	ChatID             int64
	InviterTelegramID  int64
	ChatLabel          string
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

	if cfg.StatusUpdateInterval <= 0 {
		cfg.StatusUpdateInterval = 10 * time.Minute
	}
	if cfg.InviteTokenTTL <= 0 {
		cfg.InviteTokenTTL = 24 * time.Hour
	}

	allowedChatIDs := make(map[int64]struct{}, len(cfg.TargetChatIDs))
	for _, chatID := range cfg.TargetChatIDs {
		allowedChatIDs[chatID] = struct{}{}
	}

	service := &Service{
		session:              session,
		store:                store,
		notifier:             notifier,
		logger:               logger.With("component", "discord"),
		targetGuildID:        cfg.TargetGuildID,
		targetChatIDs:        append([]int64(nil), cfg.TargetChatIDs...),
		allowedChatIDs:       allowedChatIDs,
		telegramBotUsername:  cfg.TelegramBotUsername,
		dmCooldown:           cfg.DMCooldown,
		statusUpdateInterval: cfg.StatusUpdateInterval,
		inviteTokenTTL:       cfg.InviteTokenTTL,
		lastDMAt:             make(map[string]time.Time),
		voiceStates:          make(map[string]string),
		voiceJoinedAt:        make(map[string]time.Time),
		activeSessions:       make(map[string]activeVoiceSession),
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

	backgroundCtx, cancel := context.WithCancel(context.Background())
	s.cancelBackground = cancel
	s.backgroundWG.Add(1)
	go s.runPeriodicStatusUpdates(backgroundCtx)

	s.logger.Info("discord session opened")
	return nil
}

func (s *Service) Stop() error {
	if s.cancelBackground != nil {
		s.cancelBackground()
		s.backgroundWG.Wait()
	}

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

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, voiceState := range event.Guild.VoiceStates {
		s.voiceStates[voiceState.UserID] = voiceState.ChannelID
		s.voiceJoinedAt[voiceState.UserID] = now
	}

	s.logger.Info("discord voice state cache initialized", "guild_id", event.Guild.ID, "tracked_users", len(event.Guild.VoiceStates))
}

func (s *Service) onVoiceStateUpdate(session *discordgo.Session, event *discordgo.VoiceStateUpdate) {
	// #region debug-point A:voice-state-entry
	s.logger.Info("[DEBUG] voice state update received", "hypothesis_id", "A", "discord_id", event.UserID, "guild_id", event.GuildID, "channel_id", event.ChannelID)
	// #endregion
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
	if previousChannelID == event.ChannelID {
		return
	}

	if previousChannelID != "" && s.hasActiveSession(previousChannelID) {
		if err := s.refreshChannelStatus(previousChannelID); err != nil {
			s.logger.Error("failed to refresh previous voice channel status", "channel_id", previousChannelID, "error", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mappings, err := s.store.GetMappingsByDiscordID(ctx, userID)
	if err != nil {
		s.logger.Error("failed to get user mappings", "discord_id", userID, "error", err)
		return
	}
	allowedMappings := s.filterAllowedMappings(mappings)
	// #region debug-point B:user-mappings
	s.logger.Info("[DEBUG] loaded user mappings", "hypothesis_id", "B", "discord_id", userID, "mapping_count", len(mappings), "allowed_mapping_count", len(allowedMappings), "channel_id", event.ChannelID, "previous_channel_id", previousChannelID)
	// #endregion

	if event.ChannelID == "" {
		return
	}

	if len(allowedMappings) > 0 {
		s.ensureActiveSession(event.ChannelID)
	}

	if len(allowedMappings) > 0 || s.hasActiveSession(event.ChannelID) {
		if err := s.refreshChannelStatus(event.ChannelID); err != nil {
			s.logger.Error("failed to refresh current voice channel status", "discord_id", userID, "channel_id", event.ChannelID, "error", err)
		}
	}

	existingChatIDs := make(map[int64]struct{}, len(allowedMappings))
	for _, mapping := range allowedMappings {
		existingChatIDs[mapping.TelegramChatID] = struct{}{}
	}

	invites, err := s.eligibleChatInvites(event.ChannelID, userID, existingChatIDs)
	if err != nil {
		s.logger.Error("failed to build eligible chat invites", "discord_id", userID, "channel_id", event.ChannelID, "error", err)
		return
	}
	// #region debug-point C:eligible-invites
	s.logger.Info("[DEBUG] eligible invites computed", "hypothesis_id", "C", "discord_id", userID, "channel_id", event.ChannelID, "invite_count", len(invites), "existing_chat_count", len(existingChatIDs))
	// #endregion
	if len(invites) == 0 {
		return
	}

	if !s.allowDM(userID) {
		s.logger.Debug("skipping discord dm because cooldown is active", "discord_id", userID)
		return
	}

	if err := s.sendDeepLinkDM(session, userID, invites); err != nil {
		s.logger.Warn("failed to send discord dm with deep link", "discord_id", userID, "error", err)
		return
	}

	s.logger.Info("discord dm with deep link sent", "discord_id", userID, "chat_count", len(invites))
}

func (s *Service) runPeriodicStatusUpdates(ctx context.Context) {
	defer s.backgroundWG.Done()

	ticker := time.NewTicker(s.statusUpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, channelID := range s.activeChannelIDs() {
				if err := s.refreshChannelStatus(channelID); err != nil {
					s.logger.Error("failed to refresh periodic voice status", "channel_id", channelID, "error", err)
				}
			}
		}
	}
}

func (s *Service) swapVoiceState(userID, newChannelID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	previousChannelID := s.voiceStates[userID]
	if newChannelID == "" {
		delete(s.voiceStates, userID)
		delete(s.voiceJoinedAt, userID)
	} else {
		s.voiceStates[userID] = newChannelID
		if previousChannelID != newChannelID {
			s.voiceJoinedAt[userID] = now
		}
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

func (s *Service) ensureActiveSession(channelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.activeSessions[channelID]; exists {
		return
	}

	s.activeSessions[channelID] = activeVoiceSession{
		StartedAt:     time.Now(),
		ActiveChatIDs: make(map[int64]struct{}),
	}
}

func (s *Service) hasActiveSession(channelID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, exists := s.activeSessions[channelID]
	return exists
}

func (s *Service) activeChannelIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	channelIDs := make([]string, 0, len(s.activeSessions))
	for channelID := range s.activeSessions {
		channelIDs = append(channelIDs, channelID)
	}

	sort.Strings(channelIDs)
	return channelIDs
}

func (s *Service) refreshChannelStatus(channelID string) error {
	sessionInfo, ok := s.getActiveSession(channelID)
	if !ok {
		return nil
	}

	participantIDs := s.participantIDsByChannel(channelID)
	channelName := s.channelName(channelID)
	currentChatParticipants, err := s.chatParticipants(participantIDs)
	if err != nil {
		return fmt.Errorf("build chat participants: %w", err)
	}
	// #region debug-point D:channel-status-input
	s.logger.Info("[DEBUG] refreshing channel status", "hypothesis_id", "D", "channel_id", channelID, "participant_count", len(participantIDs), "chat_count", len(currentChatParticipants))
	// #endregion

	if startedAt, ok := s.channelStartedAt(participantIDs); ok {
		sessionInfo.StartedAt = startedAt
	}

	if len(participantIDs) == 0 || len(currentChatParticipants) == 0 {
		if err := s.closeSessionStatuses(channelID, channelName, sessionInfo, sessionInfo.ActiveChatIDs); err != nil {
			return err
		}

		s.removeActiveSession(channelID)
		return nil
	}

	currentChatIDs := make(map[int64]struct{}, len(currentChatParticipants))
	for chatID, participants := range currentChatParticipants {
		sort.Strings(participants)
		currentChatIDs[chatID] = struct{}{}

		text := buildActiveVoiceStatusMessage(channelName, sessionInfo.StartedAt, participants)
		if err := s.notifier.UpsertVoiceChannelStatus(chatID, channelID, text); err != nil {
			return fmt.Errorf("upsert telegram voice status for chat %d: %w", chatID, err)
		}
	}

	closedChatIDs := differenceChatIDs(sessionInfo.ActiveChatIDs, currentChatIDs)
	if err := s.closeSessionStatuses(channelID, channelName, sessionInfo, closedChatIDs); err != nil {
		return err
	}

	sessionInfo.ActiveChatIDs = currentChatIDs
	s.setActiveSession(channelID, sessionInfo)

	return nil
}

func (s *Service) eligibleChatInvites(channelID, excludeUserID string, existingChatIDs map[int64]struct{}) ([]chatInvite, error) {
	participantIDs := s.participantIDsByChannel(channelID)
	inviteByChatID := make(map[int64]chatInvite)
	// #region debug-point E:eligible-participants
	s.logger.Info("[DEBUG] scanning eligible invite participants", "hypothesis_id", "E", "channel_id", channelID, "exclude_discord_id", excludeUserID, "participant_count", len(participantIDs), "existing_chat_count", len(existingChatIDs))
	// #endregion

	for _, participantID := range participantIDs {
		if participantID == excludeUserID {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		mappings, err := s.store.GetMappingsByDiscordID(ctx, participantID)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("load participant mappings for invites: %w", err)
		}

		for _, mapping := range s.filterAllowedMappings(mappings) {
			if _, alreadyMapped := existingChatIDs[mapping.TelegramChatID]; alreadyMapped {
				continue
			}
			if _, exists := inviteByChatID[mapping.TelegramChatID]; exists {
				continue
			}

			inviteByChatID[mapping.TelegramChatID] = chatInvite{
				ChatID:            mapping.TelegramChatID,
				InviterTelegramID: mapping.TelegramID,
				ChatLabel:         s.notifier.ChatLabel(mapping.TelegramChatID),
			}
		}
	}

	invites := make([]chatInvite, 0, len(inviteByChatID))
	for _, invite := range inviteByChatID {
		invites = append(invites, invite)
	}

	if len(invites) == 0 && len(s.targetChatIDs) == 1 {
		chatID := s.targetChatIDs[0]
		if _, alreadyMapped := existingChatIDs[chatID]; !alreadyMapped {
			invites = append(invites, chatInvite{
				ChatID:            chatID,
				InviterTelegramID: 0,
				ChatLabel:         s.notifier.ChatLabel(chatID),
			})
		}
	}

	sort.Slice(invites, func(i, j int) bool {
		return invites[i].ChatID < invites[j].ChatID
	})

	return invites, nil
}

func (s *Service) chatParticipants(participantIDs []string) (map[int64][]string, error) {
	participantsByChatID := make(map[int64][]string)

	for _, participantID := range participantIDs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		mappings, err := s.store.GetMappingsByDiscordID(ctx, participantID)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("load participant mappings: %w", err)
		}

		for _, mapping := range s.filterAllowedMappings(mappings) {
			name := strings.TrimSpace(mapping.TelegramName)
			if name == "" {
				name = fmt.Sprintf("Telegram user %d", mapping.TelegramID)
			}

			participantsByChatID[mapping.TelegramChatID] = append(participantsByChatID[mapping.TelegramChatID], name)
		}
	}

	for chatID, participants := range participantsByChatID {
		participantsByChatID[chatID] = uniqueSortedStrings(participants)
	}

	return participantsByChatID, nil
}

func (s *Service) closeSessionStatuses(channelID, channelName string, sessionInfo activeVoiceSession, chatIDs map[int64]struct{}) error {
	for chatID := range chatIDs {
		text := buildClosedVoiceStatusMessage(channelName, sessionInfo.StartedAt)
		if err := s.notifier.CloseVoiceChannelStatus(chatID, channelID, text); err != nil {
			return fmt.Errorf("close telegram voice status for chat %d: %w", chatID, err)
		}
	}

	return nil
}

func (s *Service) getActiveSession(channelID string) (activeVoiceSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionInfo, exists := s.activeSessions[channelID]
	return sessionInfo, exists
}

func (s *Service) setActiveSession(channelID string, sessionInfo activeVoiceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.activeSessions[channelID] = sessionInfo
}

func (s *Service) removeActiveSession(channelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.activeSessions, channelID)
}

func (s *Service) participantIDsByChannel(channelID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	participantIDs := make([]string, 0)
	for userID, currentChannelID := range s.voiceStates {
		if currentChannelID == channelID {
			participantIDs = append(participantIDs, userID)
		}
	}

	sort.Strings(participantIDs)
	return participantIDs
}

func (s *Service) channelStartedAt(participantIDs []string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var startedAt time.Time
	for _, participantID := range participantIDs {
		joinedAt, ok := s.voiceJoinedAt[participantID]
		if !ok || joinedAt.IsZero() {
			continue
		}
		if startedAt.IsZero() || joinedAt.Before(startedAt) {
			startedAt = joinedAt
		}
	}

	if startedAt.IsZero() {
		return time.Time{}, false
	}

	return startedAt, true
}

func (s *Service) filterAllowedMappings(mappings []storage.UserMapping) []storage.UserMapping {
	filtered := make([]storage.UserMapping, 0, len(mappings))
	for _, mapping := range mappings {
		if _, ok := s.allowedChatIDs[mapping.TelegramChatID]; ok {
			filtered = append(filtered, mapping)
		}
	}

	return filtered
}

func (s *Service) channelName(channelID string) string {
	if channel, err := s.session.State.Channel(channelID); err == nil && channel != nil && strings.TrimSpace(channel.Name) != "" {
		return channel.Name
	}

	if channel, err := s.session.Channel(channelID); err == nil && channel != nil && strings.TrimSpace(channel.Name) != "" {
		return channel.Name
	}

	return channelID
}

func (s *Service) sendDeepLinkDM(session *discordgo.Session, userID string, invites []chatInvite) error {
	if len(invites) == 0 {
		return nil
	}
	// #region debug-point F:dm-send-attempt
	s.logger.Info("[DEBUG] attempting to send discord dm", "hypothesis_id", "F", "discord_id", userID, "invite_count", len(invites))
	// #endregion

	channel, err := session.UserChannelCreate(userID)
	if err != nil {
		return fmt.Errorf("create dm channel: %w", err)
	}

	buttonRows := make([]discordgo.MessageComponent, 0, len(invites))
	for _, invite := range invites {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		inviteToken, err := s.store.CreateInviteToken(ctx, userID, invite.ChatID, invite.InviterTelegramID, s.inviteTokenTTL)
		cancel()
		if err != nil {
			return fmt.Errorf("create invite token for chat %d: %w", invite.ChatID, err)
		}

		deepLink := fmt.Sprintf("https://t.me/%s?start=auth_%s", s.telegramBotUsername, inviteToken.Token)
		buttonRows = append(buttonRows, discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label: truncateButtonLabel("Привязать: " + invite.ChatLabel),
					Style: discordgo.LinkButton,
					URL:   deepLink,
				},
			},
		})
	}

	content := "Привет! Для тебя доступны привязки только к тем Telegram-беседам, участники которых уже находятся с тобой в голосовом канале. Выбери подходящую беседу по кнопке ниже."
	message := &discordgo.MessageSend{
		Content:    content,
		Components: buttonRows,
	}

	if _, err := session.ChannelMessageSendComplex(channel.ID, message); err != nil {
		return fmt.Errorf("send deep link dm: %w", err)
	}
	// #region debug-point F:dm-send-success
	s.logger.Info("[DEBUG] discord dm sent successfully", "hypothesis_id", "F", "discord_id", userID, "channel_id", channel.ID, "invite_count", len(invites))
	// #endregion

	return nil
}

func differenceChatIDs(previous, current map[int64]struct{}) map[int64]struct{} {
	result := make(map[int64]struct{})
	for chatID := range previous {
		if _, ok := current[chatID]; !ok {
			result[chatID] = struct{}{}
		}
	}

	return result
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}

	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		unique[trimmed] = struct{}{}
	}

	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}

	sort.Strings(result)
	return result
}

func truncateButtonLabel(label string) string {
	const maxLen = 80
	if len(label) <= maxLen {
		return label
	}

	return label[:maxLen]
}

func buildActiveVoiceStatusMessage(channelName string, startedAt time.Time, participants []string) string {
	var builder strings.Builder

	builder.WriteString("🎙 В голосовом канале Discord кто-то сидит\n")
	builder.WriteString(fmt.Sprintf("Канал: %s\n", channelName))
	builder.WriteString(fmt.Sprintf("⏱ В чате: %s\n", formatDuration(time.Since(startedAt))))
	builder.WriteString(fmt.Sprintf("👥 Участники (%d):\n", len(participants)))

	for _, participant := range participants {
		builder.WriteString("- ")
		builder.WriteString(participant)
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String())
}

func buildClosedVoiceStatusMessage(channelName string, startedAt time.Time) string {
	return fmt.Sprintf(
		"🔇 Голосовой канал Discord опустел\nКанал: %s\n⏱ Провели в чате: %s",
		channelName,
		formatDuration(time.Since(startedAt)),
	)
}

func formatDuration(duration time.Duration) string {
	if duration < time.Minute {
		return "меньше минуты"
	}

	totalMinutes := int(duration / time.Minute)
	days := totalMinutes / (24 * 60)
	hours := (totalMinutes % (24 * 60)) / 60
	minutes := totalMinutes % 60

	parts := make([]string, 0, 3)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d д", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d ч", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d мин", minutes))
	}

	return strings.Join(parts, " ")
}
