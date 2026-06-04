package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type UserMapping struct {
	DiscordID      string
	TelegramChatID int64
	TelegramID     int64
	TelegramName   string
}

type InviteToken struct {
	Token             string
	DiscordID         string
	TelegramChatID    int64
	InviterTelegramID int64
	ExpiresAt         time.Time
	UsedAt            *time.Time
}

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(databasePath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Init(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS chat_user_mappings (
	discord_id TEXT NOT NULL,
	telegram_chat_id INTEGER NOT NULL,
	telegram_id INTEGER NOT NULL,
	telegram_name TEXT NOT NULL,
	PRIMARY KEY (discord_id, telegram_chat_id)
);

CREATE TABLE IF NOT EXISTS invite_tokens (
	token TEXT PRIMARY KEY,
	discord_id TEXT NOT NULL,
	telegram_chat_id INTEGER NOT NULL,
	inviter_telegram_id INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at INTEGER
);`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create sqlite schema: %w", err)
	}

	return nil
}

func (s *SQLiteStore) MigrateLegacyMappings(ctx context.Context, defaultTelegramChatID int64) error {
	if defaultTelegramChatID == 0 {
		return nil
	}

	var exists int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'user_mappings';`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check legacy user_mappings table: %w", err)
	}
	if exists == 0 {
		return nil
	}

	const migrateQuery = `
INSERT OR IGNORE INTO chat_user_mappings (discord_id, telegram_chat_id, telegram_id, telegram_name)
SELECT discord_id, ?, telegram_id, telegram_name
FROM user_mappings;`

	if _, err := s.db.ExecContext(ctx, migrateQuery, defaultTelegramChatID); err != nil {
		return fmt.Errorf("migrate legacy user mappings: %w", err)
	}

	return nil
}

func (s *SQLiteStore) UpsertMapping(ctx context.Context, mapping UserMapping) error {
	const query = `
INSERT INTO chat_user_mappings (discord_id, telegram_chat_id, telegram_id, telegram_name)
VALUES (?, ?, ?, ?)
ON CONFLICT(discord_id, telegram_chat_id) DO UPDATE SET
	telegram_id = excluded.telegram_id,
	telegram_name = excluded.telegram_name;`

	if _, err := s.db.ExecContext(ctx, query, mapping.DiscordID, mapping.TelegramChatID, mapping.TelegramID, mapping.TelegramName); err != nil {
		return fmt.Errorf("upsert user mapping: %w", err)
	}

	return nil
}

func (s *SQLiteStore) GetMappingsByDiscordID(ctx context.Context, discordID string) ([]UserMapping, error) {
	const query = `
SELECT discord_id, telegram_chat_id, telegram_id, telegram_name
FROM chat_user_mappings
WHERE discord_id = ?;`

	rows, err := s.db.QueryContext(ctx, query, discordID)
	if err != nil {
		return nil, fmt.Errorf("query user mappings by discord id: %w", err)
	}
	defer rows.Close()

	mappings := make([]UserMapping, 0)
	for rows.Next() {
		var mapping UserMapping
		if err := rows.Scan(&mapping.DiscordID, &mapping.TelegramChatID, &mapping.TelegramID, &mapping.TelegramName); err != nil {
			return nil, fmt.Errorf("scan user mapping by discord id: %w", err)
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user mappings by discord id: %w", err)
	}

	return mappings, nil
}

func (s *SQLiteStore) GetMappingsByTelegramID(ctx context.Context, telegramID int64) ([]UserMapping, error) {
	const query = `
SELECT discord_id, telegram_chat_id, telegram_id, telegram_name
FROM chat_user_mappings
WHERE telegram_id = ?;`

	rows, err := s.db.QueryContext(ctx, query, telegramID)
	if err != nil {
		return nil, fmt.Errorf("query user mappings by telegram id: %w", err)
	}
	defer rows.Close()

	mappings := make([]UserMapping, 0)
	for rows.Next() {
		var mapping UserMapping
		if err := rows.Scan(&mapping.DiscordID, &mapping.TelegramChatID, &mapping.TelegramID, &mapping.TelegramName); err != nil {
			return nil, fmt.Errorf("scan user mapping by telegram id: %w", err)
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user mappings by telegram id: %w", err)
	}

	return mappings, nil
}

func (s *SQLiteStore) DeleteMappingsByTelegramUser(ctx context.Context, telegramChatID, telegramID int64) (int64, error) {
	const query = `
DELETE FROM chat_user_mappings
WHERE telegram_chat_id = ? AND telegram_id = ?;`

	result, err := s.db.ExecContext(ctx, query, telegramChatID, telegramID)
	if err != nil {
		return 0, fmt.Errorf("delete user mappings by telegram user: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("check deleted user mapping rows affected: %w", err)
	}

	return rowsAffected, nil
}

func (s *SQLiteStore) CreateInviteToken(ctx context.Context, discordID string, telegramChatID, inviterTelegramID int64, ttl time.Duration) (*InviteToken, error) {
	tokenValue, err := newTokenValue()
	if err != nil {
		return nil, fmt.Errorf("generate invite token: %w", err)
	}

	expiresAt := time.Now().Add(ttl)
	inviteToken := &InviteToken{
		Token:             tokenValue,
		DiscordID:         discordID,
		TelegramChatID:    telegramChatID,
		InviterTelegramID: inviterTelegramID,
		ExpiresAt:         expiresAt,
	}

	const query = `
INSERT INTO invite_tokens (token, discord_id, telegram_chat_id, inviter_telegram_id, expires_at, used_at)
VALUES (?, ?, ?, ?, ?, NULL);`

	if _, err := s.db.ExecContext(ctx, query, inviteToken.Token, inviteToken.DiscordID, inviteToken.TelegramChatID, inviteToken.InviterTelegramID, inviteToken.ExpiresAt.Unix()); err != nil {
		return nil, fmt.Errorf("insert invite token: %w", err)
	}

	return inviteToken, nil
}

func (s *SQLiteStore) GetInviteToken(ctx context.Context, token string) (*InviteToken, error) {
	const query = `
SELECT token, discord_id, telegram_chat_id, inviter_telegram_id, expires_at, used_at
FROM invite_tokens
WHERE token = ?;`

	var inviteToken InviteToken
	var expiresAtUnix int64
	var usedAtUnix sql.NullInt64
	if err := s.db.QueryRowContext(ctx, query, token).Scan(
		&inviteToken.Token,
		&inviteToken.DiscordID,
		&inviteToken.TelegramChatID,
		&inviteToken.InviterTelegramID,
		&expiresAtUnix,
		&usedAtUnix,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, fmt.Errorf("select invite token: %w", err)
	}

	inviteToken.ExpiresAt = time.Unix(expiresAtUnix, 0)
	if usedAtUnix.Valid {
		usedAt := time.Unix(usedAtUnix.Int64, 0)
		inviteToken.UsedAt = &usedAt
	}

	return &inviteToken, nil
}

func (s *SQLiteStore) ConsumeInviteToken(ctx context.Context, token string) (bool, error) {
	const query = `
UPDATE invite_tokens
SET used_at = ?
WHERE token = ? AND used_at IS NULL;`

	result, err := s.db.ExecContext(ctx, query, time.Now().Unix(), token)
	if err != nil {
		return false, fmt.Errorf("consume invite token: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check invite token rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

func newTokenValue() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}

	return hex.EncodeToString(buffer), nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
