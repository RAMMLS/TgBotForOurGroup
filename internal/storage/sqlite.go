package storage

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type UserMapping struct {
	DiscordID    string
	TelegramID   int64
	TelegramName string
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
	const query = `
CREATE TABLE IF NOT EXISTS user_mappings (
	discord_id TEXT PRIMARY KEY,
	telegram_id INTEGER NOT NULL,
	telegram_name TEXT NOT NULL
);`

	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("create user_mappings table: %w", err)
	}

	return nil
}

func (s *SQLiteStore) UpsertMapping(ctx context.Context, mapping UserMapping) error {
	const query = `
INSERT INTO user_mappings (discord_id, telegram_id, telegram_name)
VALUES (?, ?, ?)
ON CONFLICT(discord_id) DO UPDATE SET
	telegram_id = excluded.telegram_id,
	telegram_name = excluded.telegram_name;`

	if _, err := s.db.ExecContext(ctx, query, mapping.DiscordID, mapping.TelegramID, mapping.TelegramName); err != nil {
		return fmt.Errorf("upsert user mapping: %w", err)
	}

	return nil
}

func (s *SQLiteStore) GetByDiscordID(ctx context.Context, discordID string) (*UserMapping, error) {
	const query = `
SELECT discord_id, telegram_id, telegram_name
FROM user_mappings
WHERE discord_id = ?;`

	var mapping UserMapping
	if err := s.db.QueryRowContext(ctx, query, discordID).Scan(&mapping.DiscordID, &mapping.TelegramID, &mapping.TelegramName); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, fmt.Errorf("select user mapping by discord id: %w", err)
	}

	return &mapping, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
