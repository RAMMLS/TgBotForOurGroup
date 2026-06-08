package media

import (
	"io"
	"log/slog"
	"math/rand"
	"testing"
	"time"
)

func TestBuildCandidatesPrefersMatchingTags(t *testing.T) {
	service := &Service{
		random: rand.New(rand.NewSource(1)),
	}

	candidates := service.buildCandidates([]Item{
		{Kind: KindSticker, Source: "laugh", Tags: []string{"laugh"}, Weight: 1},
		{Kind: KindSticker, Source: "cringe", Tags: []string{"cringe"}, Weight: 5},
	}, []string{"laugh", "meme"})

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate after tag filtering, got %d", len(candidates))
	}
	if candidates[0].Item.Source != "laugh" {
		t.Fatalf("expected laugh candidate, got %q", candidates[0].Item.Source)
	}
}

func TestDecideSmokeUsesImportedLibraryForHighSignalMessage(t *testing.T) {
	service := &Service{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:      true,
		cooldown:     0,
		minScore:     0,
		repeatWindow: time.Hour,
		repeatLimit:  10,
		random:       rand.New(rand.NewSource(1)),
		lastSentAt:   map[int64]time.Time{},
		recentByID:   map[int64][]time.Time{},
		sentByChat:   map[int64][]sentRecord{},
	}
	service.ReplaceLibrary([]Item{
		{Kind: KindAnimation, Source: "anim-laugh", Tags: []string{"laugh", "meme"}, Group: "memes", Weight: 1},
		{Kind: KindSticker, Source: "sticker-hype", Tags: []string{"hype"}, Group: "stickers", Weight: 1},
	})

	decision := service.Decide(MessageContext{
		ChatID: 42,
		UserID: 7,
		Text:   "АХАХА ЖЕСТЬ ЭТО ЧТО ТАКОЕ!!!!",
	})
	if decision == nil {
		t.Fatal("expected decision for strong meme signal")
	}
	if decision.Item.Kind != KindAnimation {
		t.Fatalf("expected animation to be preferred for laugh signal, got %q", decision.Item.Kind)
	}
	if decision.Item.Source != "anim-laugh" {
		t.Fatalf("expected imported animation source, got %q", decision.Item.Source)
	}
	if decision.Reason == "" {
		t.Fatal("expected non-empty decision reason")
	}
}

func TestDecideAvoidsImmediateRepeatBySourceAndGroup(t *testing.T) {
	now := time.Now()
	service := &Service{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:      true,
		cooldown:     0,
		minScore:     0,
		repeatWindow: time.Hour,
		repeatLimit:  10,
		random:       rand.New(rand.NewSource(1)),
		stickerItems: []Item{
			{Kind: KindSticker, Source: "same-source", Tags: []string{"laugh"}, Group: "pack-a", Weight: 1},
			{Kind: KindSticker, Source: "same-pack-new-source", Tags: []string{"laugh"}, Group: "pack-a", Weight: 1},
			{Kind: KindSticker, Source: "fresh-pack", Tags: []string{"laugh"}, Group: "pack-b", Weight: 1},
		},
		lastSentAt: map[int64]time.Time{},
		recentByID: map[int64][]time.Time{},
		sentByChat: map[int64][]sentRecord{
			777: {{
				SentAt: now.Add(-time.Minute),
				Source: "same-source",
				Group:  "pack-a",
				Kind:   KindSticker,
			}},
		},
	}

	decision := service.Decide(MessageContext{
		ChatID: 777,
		UserID: 1,
		Text:   "АХАХА это слишком сильно!!",
	})
	if decision == nil {
		t.Fatal("expected decision to be produced")
	}
	if decision.Item.Source != "fresh-pack" {
		t.Fatalf("expected fresh-pack to avoid repeats, got %q", decision.Item.Source)
	}
}
