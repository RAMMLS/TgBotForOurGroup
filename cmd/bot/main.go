package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	discordsvc "tgbotforourgroup/internal/discord"
	"tgbotforourgroup/internal/storage"
	telegramsvc "tgbotforourgroup/internal/telegram"
)

const defaultDatabasePath = "bot.db"

type config struct {
	DiscordBotToken      string
	DiscordTargetGuildID string
	TelegramBotToken     string
	TelegramBotUsername  string
	TelegramTargetChatID int64
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger); err != nil {
		logger.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if err := godotenv.Load(); err != nil {
		logger.Info(".env file not loaded", "error", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.NewSQLiteStore(databasePath())
	if err != nil {
		return fmt.Errorf("create sqlite store: %w", err)
	}
	defer closeWithLog(logger, "sqlite store", store.Close)

	initCtx, cancelInit := context.WithTimeout(context.Background(), 5*time.Second)
	if err := store.Init(initCtx); err != nil {
		cancelInit()
		return fmt.Errorf("initialize sqlite schema: %w", err)
	}
	cancelInit()

	telegramService, err := telegramsvc.NewService(cfg.TelegramBotToken, cfg.TelegramTargetChatID, store, logger)
	if err != nil {
		return fmt.Errorf("create telegram service: %w", err)
	}

	discordService, err := discordsvc.NewService(discordsvc.Config{
		Token:               cfg.DiscordBotToken,
		TargetGuildID:       cfg.DiscordTargetGuildID,
		TelegramBotUsername: cfg.TelegramBotUsername,
		DMCooldown:          10 * time.Minute,
	}, store, telegramService, logger)
	if err != nil {
		return fmt.Errorf("create discord service: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		telegramService.Start()
	}()

	if err := discordService.Start(); err != nil {
		telegramService.Stop()
		wg.Wait()
		return fmt.Errorf("start discord service: %w", err)
	}

	logger.Info("integration service started")

	<-ctx.Done()
	logger.Info("shutdown signal received")

	telegramService.Stop()
	if err := discordService.Stop(); err != nil {
		logger.Error("failed to stop discord service", "error", err)
	}
	wg.Wait()

	logger.Info("integration service stopped")
	return nil
}

func loadConfig() (config, error) {
	discordBotToken, err := requireEnv("DISCORD_BOT_TOKEN")
	if err != nil {
		return config{}, err
	}

	discordTargetGuildID, err := requireEnv("DISCORD_TARGET_GUILD_ID")
	if err != nil {
		return config{}, err
	}

	telegramBotToken, err := requireEnv("TELEGRAM_BOT_TOKEN")
	if err != nil {
		return config{}, err
	}

	telegramBotUsername, err := requireEnv("TELEGRAM_BOT_USERNAME")
	if err != nil {
		return config{}, err
	}

	chatIDRaw, err := requireEnv("TELEGRAM_TARGET_CHAT_ID")
	if err != nil {
		return config{}, err
	}

	chatID, err := strconv.ParseInt(chatIDRaw, 10, 64)
	if err != nil {
		return config{}, fmt.Errorf("parse TELEGRAM_TARGET_CHAT_ID: %w", err)
	}

	return config{
		DiscordBotToken:      discordBotToken,
		DiscordTargetGuildID: discordTargetGuildID,
		TelegramBotToken:     telegramBotToken,
		TelegramBotUsername:  strings.TrimPrefix(telegramBotUsername, "@"),
		TelegramTargetChatID: chatID,
	}, nil
}

func requireEnv(key string) (string, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}

	return value, nil
}

func databasePath() string {
	if value, ok := os.LookupEnv("SQLITE_PATH"); ok && strings.TrimSpace(value) != "" {
		return value
	}

	return defaultDatabasePath
}

func closeWithLog(logger *slog.Logger, name string, closeFn func() error) {
	if err := closeFn(); err != nil {
		logger.Error("failed to close resource", "resource", name, "error", err)
	}
}
