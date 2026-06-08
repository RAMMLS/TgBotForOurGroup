package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteStoreSmokeMediaTagsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	defer store.Close()

	if err := store.Init(ctx); err != nil {
		t.Fatalf("init sqlite store: %v", err)
	}

	collection, err := store.CreateMediaCollection(ctx, 1001, "Smoke Pack", "telegram_sticker_set", "smoke-pack", true)
	if err != nil {
		t.Fatalf("create media collection: %v", err)
	}

	if err := store.AddMediaItems(ctx, collection.ID, []MediaItem{
		{
			Kind:   "sticker",
			Source: "sticker-file-id",
			Tags:   "laugh,meme",
			Weight: 2,
		},
		{
			Kind:   "animation",
			Source: "animation-file-id",
			Tags:   "laugh,reaction",
			Weight: 1,
		},
	}); err != nil {
		t.Fatalf("add media items: %v", err)
	}

	items, err := store.ListEnabledMediaItems(ctx)
	if err != nil {
		t.Fatalf("list enabled media items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 enabled media items, got %d", len(items))
	}

	first := items[0]
	if first.CollectionName != "Smoke Pack" {
		t.Fatalf("expected collection name to round trip, got %q", first.CollectionName)
	}
	if first.Tags == "" {
		t.Fatal("expected tags to be stored and loaded")
	}
}
