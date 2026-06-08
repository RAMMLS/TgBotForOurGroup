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

type MediaCollection struct {
	ID              int64
	OwnerTelegramID int64
	Name            string
	SourceType      string
	SourceRef       string
	Enabled         bool
	CreatedAt       time.Time
	ItemCount       int
}

type MediaItem struct {
	ID           int64
	CollectionID int64
	Kind         string
	Source       string
	Caption      string
	Weight       int
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
);

CREATE TABLE IF NOT EXISTS media_collections (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_telegram_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	source_type TEXT NOT NULL,
	source_ref TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS media_items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	kind TEXT NOT NULL,
	source TEXT NOT NULL,
	caption TEXT NOT NULL DEFAULT '',
	weight INTEGER NOT NULL DEFAULT 1,
	UNIQUE(collection_id, kind, source),
	FOREIGN KEY(collection_id) REFERENCES media_collections(id) ON DELETE CASCADE
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

func (s *SQLiteStore) CreateMediaCollection(ctx context.Context, ownerTelegramID int64, name, sourceType, sourceRef string, enabled bool) (*MediaCollection, error) {
	now := time.Now()
	const query = `
INSERT INTO media_collections (owner_telegram_id, name, source_type, source_ref, enabled, created_at)
VALUES (?, ?, ?, ?, ?, ?);`

	result, err := s.db.ExecContext(ctx, query, ownerTelegramID, name, sourceType, sourceRef, boolToInt(enabled), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("insert media collection: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get media collection id: %w", err)
	}

	return &MediaCollection{
		ID:              id,
		OwnerTelegramID: ownerTelegramID,
		Name:            name,
		SourceType:      sourceType,
		SourceRef:       sourceRef,
		Enabled:         enabled,
		CreatedAt:       now,
	}, nil
}

func (s *SQLiteStore) ListMediaCollectionsByOwner(ctx context.Context, ownerTelegramID int64) ([]MediaCollection, error) {
	const query = `
SELECT
	c.id,
	c.owner_telegram_id,
	c.name,
	c.source_type,
	c.source_ref,
	c.enabled,
	c.created_at,
	COUNT(i.id) AS item_count
FROM media_collections c
LEFT JOIN media_items i ON i.collection_id = c.id
WHERE c.owner_telegram_id = ?
GROUP BY c.id, c.owner_telegram_id, c.name, c.source_type, c.source_ref, c.enabled, c.created_at
ORDER BY c.created_at DESC, c.id DESC;`

	rows, err := s.db.QueryContext(ctx, query, ownerTelegramID)
	if err != nil {
		return nil, fmt.Errorf("query media collections: %w", err)
	}
	defer rows.Close()

	collections := make([]MediaCollection, 0)
	for rows.Next() {
		var collection MediaCollection
		var enabled int
		var createdAtUnix int64
		if err := rows.Scan(
			&collection.ID,
			&collection.OwnerTelegramID,
			&collection.Name,
			&collection.SourceType,
			&collection.SourceRef,
			&enabled,
			&createdAtUnix,
			&collection.ItemCount,
		); err != nil {
			return nil, fmt.Errorf("scan media collection: %w", err)
		}

		collection.Enabled = enabled != 0
		collection.CreatedAt = time.Unix(createdAtUnix, 0)
		collections = append(collections, collection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media collections: %w", err)
	}

	return collections, nil
}

func (s *SQLiteStore) GetMediaCollectionBySource(ctx context.Context, ownerTelegramID int64, sourceType, sourceRef string) (*MediaCollection, error) {
	if sourceType == "" || sourceRef == "" {
		return nil, nil
	}

	const query = `
SELECT
	c.id,
	c.owner_telegram_id,
	c.name,
	c.source_type,
	c.source_ref,
	c.enabled,
	c.created_at,
	COUNT(i.id) AS item_count
FROM media_collections c
LEFT JOIN media_items i ON i.collection_id = c.id
WHERE c.owner_telegram_id = ? AND c.source_type = ? AND c.source_ref = ?
GROUP BY c.id, c.owner_telegram_id, c.name, c.source_type, c.source_ref, c.enabled, c.created_at
LIMIT 1;`

	var collection MediaCollection
	var enabled int
	var createdAtUnix int64
	err := s.db.QueryRowContext(ctx, query, ownerTelegramID, sourceType, sourceRef).Scan(
		&collection.ID,
		&collection.OwnerTelegramID,
		&collection.Name,
		&collection.SourceType,
		&collection.SourceRef,
		&enabled,
		&createdAtUnix,
		&collection.ItemCount,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query media collection by source: %w", err)
	}

	collection.Enabled = enabled != 0
	collection.CreatedAt = time.Unix(createdAtUnix, 0)
	return &collection, nil
}

func (s *SQLiteStore) SetMediaCollectionEnabled(ctx context.Context, collectionID, ownerTelegramID int64, enabled bool) error {
	const query = `
UPDATE media_collections
SET enabled = ?
WHERE id = ? AND owner_telegram_id = ?;`

	result, err := s.db.ExecContext(ctx, query, boolToInt(enabled), collectionID, ownerTelegramID)
	if err != nil {
		return fmt.Errorf("update media collection enabled: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check media collection rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (s *SQLiteStore) DeleteMediaCollection(ctx context.Context, collectionID, ownerTelegramID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete media collection tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM media_items WHERE collection_id = ?;`, collectionID); err != nil {
		return fmt.Errorf("delete media items by collection: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM media_collections WHERE id = ? AND owner_telegram_id = ?;`, collectionID, ownerTelegramID)
	if err != nil {
		return fmt.Errorf("delete media collection: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted media collection rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete media collection tx: %w", err)
	}

	return nil
}

func (s *SQLiteStore) AddMediaItems(ctx context.Context, collectionID int64, items []MediaItem) error {
	if len(items) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin media items tx: %w", err)
	}
	defer tx.Rollback()

	const query = `
INSERT OR IGNORE INTO media_items (collection_id, kind, source, caption, weight)
VALUES (?, ?, ?, ?, ?);`

	for _, item := range items {
		if _, err := tx.ExecContext(ctx, query, collectionID, item.Kind, item.Source, item.Caption, normalizedWeight(item.Weight)); err != nil {
			return fmt.Errorf("insert media item: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit media items tx: %w", err)
	}

	return nil
}

func (s *SQLiteStore) ListEnabledMediaItems(ctx context.Context) ([]MediaItem, error) {
	const query = `
SELECT i.id, i.collection_id, i.kind, i.source, i.caption, i.weight
FROM media_items i
INNER JOIN media_collections c ON c.id = i.collection_id
WHERE c.enabled = 1
ORDER BY i.id ASC;`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query enabled media items: %w", err)
	}
	defer rows.Close()

	items := make([]MediaItem, 0)
	for rows.Next() {
		var item MediaItem
		if err := rows.Scan(&item.ID, &item.CollectionID, &item.Kind, &item.Source, &item.Caption, &item.Weight); err != nil {
			return nil, fmt.Errorf("scan enabled media item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enabled media items: %w", err)
	}

	return items, nil
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

func boolToInt(value bool) int {
	if value {
		return 1
	}

	return 0
}

func normalizedWeight(weight int) int {
	if weight <= 0 {
		return 1
	}

	return weight
}
