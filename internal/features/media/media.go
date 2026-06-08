package media

import (
	"log/slog"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	KindText      Kind = "text"
	KindSticker   Kind = "sticker"
	KindAnimation Kind = "animation"
	KindVoice     Kind = "voice"
	KindAudio     Kind = "audio"
)

type Item struct {
	Kind    Kind
	Source  string
	Caption string
	Tags    []string
	Group   string
	Weight  int
}

type Decision struct {
	Item   Item
	Score  float64
	Reason string
}

type MessageContext struct {
	ChatID       int64
	UserID       int64
	Text         string
	IsReply      bool
	IsDirectChat bool
}

type Service struct {
	logger       *slog.Logger
	enabled      bool
	cooldown     time.Duration
	minScore     float64
	repeatWindow time.Duration
	repeatLimit  int
	random       *rand.Rand
	textItems    []Item
	stickerItems []Item
	animateItems []Item
	voiceItems   []Item
	audioItems   []Item
	libraryItems []Item

	mu         sync.Mutex
	lastSentAt map[int64]time.Time
	recentByID map[int64][]time.Time
	sentByChat map[int64][]sentRecord
}

type sentRecord struct {
	SentAt time.Time
	Source string
	Group  string
	Kind   Kind
}

type itemCandidate struct {
	Item          Item
	AdjustedWeight int
}

func NewServiceFromEnv(logger *slog.Logger) *Service {
	service := &Service{
		logger:       logger.With("component", "media"),
		enabled:      envBool("MEDIA_ENABLED", true),
		cooldown:     time.Duration(envInt("MEDIA_COOLDOWN_SEC", 120)) * time.Second,
		minScore:     envFloat("MEDIA_MIN_SCORE", 2.4),
		repeatWindow: time.Duration(envInt("MEDIA_REPEAT_WINDOW_SEC", 1800)) * time.Second,
		repeatLimit:  envInt("MEDIA_REPEAT_LIMIT", 12),
		random:       rand.New(rand.NewSource(time.Now().UnixNano())),
		lastSentAt:   make(map[int64]time.Time),
		recentByID:   make(map[int64][]time.Time),
		sentByChat:   make(map[int64][]sentRecord),
		textItems:    buildItems(KindText, splitPipeSeparated(os.Getenv("MEDIA_TEXT_REPLIES"))),
		stickerItems: buildItems(KindSticker, splitCommaSeparated(os.Getenv("MEDIA_STICKER_FILE_IDS"))),
		animateItems: buildItems(KindAnimation, splitCommaSeparated(os.Getenv("MEDIA_ANIMATION_FILE_IDS"))),
		voiceItems:   buildItems(KindVoice, splitCommaSeparated(os.Getenv("MEDIA_VOICE_FILE_IDS"))),
		audioItems:   buildItems(KindAudio, splitCommaSeparated(os.Getenv("MEDIA_AUDIO_FILE_IDS"))),
	}

	if len(service.textItems) == 0 {
		service.textItems = buildItems(KindText, []string{
			"Это звучит как достойный мемный момент.",
			"Записываю это в золотой фонд беседы.",
			"Тут по атмосфере просится мемчик.",
			"Сильное сообщение. Чат одобряет.",
		})
	}

	return service
}

func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

func (s *Service) ReplaceLibrary(items []Item) {
	if s == nil {
		return
	}

	filtered := make([]Item, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Source) == "" {
			continue
		}
		if item.Weight <= 0 {
			item.Weight = 1
		}
		item.Tags = uniqueStrings(item.Tags)
		item.Group = strings.TrimSpace(item.Group)
		filtered = append(filtered, item)
	}

	s.mu.Lock()
	s.libraryItems = filtered
	s.mu.Unlock()
}

func (s *Service) Decide(ctx MessageContext) *Decision {
	if s == nil || !s.enabled {
		return nil
	}

	text := strings.TrimSpace(ctx.Text)
	if text == "" || strings.HasPrefix(text, "/") {
		return nil
	}

	now := time.Now()
	normalized := normalizeText(text)

	s.mu.Lock()
	recentMessages := append(s.recentByID[ctx.ChatID], now)
	recentMessages = pruneRecent(recentMessages, now.Add(-45*time.Second))
	s.recentByID[ctx.ChatID] = recentMessages

	lastSentAt := s.lastSentAt[ctx.ChatID]
	sentHistory := pruneSentRecords(s.sentByChat[ctx.ChatID], now.Add(-s.repeatWindow), s.repeatLimit)
	s.sentByChat[ctx.ChatID] = sentHistory
	s.mu.Unlock()

	if !lastSentAt.IsZero() && now.Sub(lastSentAt) < s.cooldown {
		return nil
	}

	score, reason, messageTags := s.scoreMessage(normalized, text, len(recentMessages), ctx)
	if score < s.minScore {
		return nil
	}

	chance := 0.18 + minFloat(0.67, score*0.10)
	if score >= 5.8 {
		chance = 1
	}
	if s.random.Float64() > chance {
		return nil
	}

	item, ok := s.pickItem(normalized, score, messageTags, sentHistory)
	if !ok {
		return nil
	}

	s.mu.Lock()
	s.lastSentAt[ctx.ChatID] = now
	s.sentByChat[ctx.ChatID] = append(s.sentByChat[ctx.ChatID], sentRecord{
		SentAt: now,
		Source: item.Source,
		Group:  item.Group,
		Kind:   item.Kind,
	})
	s.mu.Unlock()

	return &Decision{
		Item:   item,
		Score:  score,
		Reason: reason,
	}
}

func (s *Service) scoreMessage(normalized, original string, recentCount int, ctx MessageContext) (float64, string, []string) {
	score := 0.0
	reasons := make([]string, 0, 6)
	messageTags := make([]string, 0, 8)

	if containsAny(normalized, laughTokens...) {
		score += 2.6
		reasons = append(reasons, "laugh")
		messageTags = append(messageTags, "laugh", "meme")
	}
	if containsAny(normalized, hypeTokens...) {
		score += 1.7
		reasons = append(reasons, "hype")
		messageTags = append(messageTags, "hype", "reaction")
	}
	if strings.Contains(original, "??") || strings.Contains(original, "!!") || strings.Contains(original, "?!") {
		score += 1.1
		reasons = append(reasons, "punctuation")
		messageTags = append(messageTags, "reaction")
	}
	if capsRatio(original) >= 0.45 && len([]rune(original)) >= 6 {
		score += 1.0
		reasons = append(reasons, "caps")
		messageTags = append(messageTags, "rage", "reaction")
	}
	if recentCount >= 3 {
		score += minFloat(1.5, float64(recentCount-2)*0.35)
		reasons = append(reasons, "chat-heat")
		messageTags = append(messageTags, "chat-heat")
	}
	if ctx.IsReply {
		score += 0.4
		reasons = append(reasons, "reply")
		messageTags = append(messageTags, "reply", "reaction")
	}
	if ctx.IsDirectChat {
		score -= 0.5
	}
	if strings.Contains(normalized, "http://") || strings.Contains(normalized, "https://") {
		score -= 0.4
	}
	if containsAny(normalized, soundTokens...) {
		messageTags = append(messageTags, "sound")
	}

	length := len([]rune(strings.TrimSpace(original)))
	switch {
	case length >= 80:
		score += 0.4
		messageTags = append(messageTags, "story")
	case length <= 3:
		score -= 0.8
	}

	if len(reasons) == 0 {
		reasons = append(reasons, "statistical-vibe")
	}

	return score, strings.Join(reasons, ","), uniqueStrings(messageTags)
}

func (s *Service) pickItem(normalized string, score float64, messageTags []string, sentHistory []sentRecord) (Item, bool) {
	preferredKinds := make([]Kind, 0, 5)

	if containsAny(normalized, soundTokens...) {
		preferredKinds = append(preferredKinds, KindVoice, KindAudio, KindAnimation, KindSticker, KindText)
	} else if containsAny(normalized, laughTokens...) || hasAnyTag(messageTags, "laugh") {
		preferredKinds = append(preferredKinds, KindAnimation, KindSticker, KindText, KindVoice, KindAudio)
	} else if score >= 4.2 {
		preferredKinds = append(preferredKinds, KindSticker, KindAnimation, KindText, KindVoice, KindAudio)
	} else {
		preferredKinds = append(preferredKinds, KindText, KindSticker, KindAnimation, KindVoice, KindAudio)
	}

	for _, kind := range preferredKinds {
		candidates := s.buildCandidates(s.itemsByKind(kind), messageTags)
		candidates = filterRepeatedCandidates(candidates, sentHistory)
		if item, ok := s.pickWeighted(candidates); ok {
			return item.Item, true
		}
	}

	return Item{}, false
}

func (s *Service) itemsByKind(kind Kind) []Item {
	s.mu.Lock()
	library := append([]Item(nil), s.libraryItems...)
	s.mu.Unlock()

	filteredLibrary := filterItemsByKind(library, kind)
	switch kind {
	case KindText:
		return append(append([]Item(nil), s.textItems...), filteredLibrary...)
	case KindSticker:
		return append(append([]Item(nil), s.stickerItems...), filteredLibrary...)
	case KindAnimation:
		return append(append([]Item(nil), s.animateItems...), filteredLibrary...)
	case KindVoice:
		return append(append([]Item(nil), s.voiceItems...), filteredLibrary...)
	case KindAudio:
		return append(append([]Item(nil), s.audioItems...), filteredLibrary...)
	default:
		return nil
	}
}

func (s *Service) buildCandidates(items []Item, messageTags []string) []itemCandidate {
	if len(items) == 0 {
		return nil
	}

	bySource := make(map[string]itemCandidate, len(items))
	matched := make([]itemCandidate, 0, len(items))
	neutral := make([]itemCandidate, 0, len(items))
	fallback := make([]itemCandidate, 0, len(items))
	tagSet := make(map[string]struct{}, len(messageTags))
	for _, tag := range messageTags {
		tagSet[tag] = struct{}{}
	}

	for _, item := range items {
		candidate := itemCandidate{
			Item:          item,
			AdjustedWeight: normalizedWeight(item.Weight),
		}
		matchCount := overlapCount(item.Tags, tagSet)
		switch {
		case matchCount > 0:
			candidate.AdjustedWeight += matchCount * 3
			matched = append(matched, candidate)
		case len(item.Tags) == 0:
			neutral = append(neutral, candidate)
		default:
			fallback = append(fallback, candidate)
		}
		bySource[item.Source] = candidate
	}

	candidates := matched
	if len(candidates) == 0 {
		candidates = neutral
	}
	if len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		for _, item := range items {
			candidates = append(candidates, bySource[item.Source])
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].AdjustedWeight > candidates[j].AdjustedWeight
	})

	return candidates
}

func (s *Service) pickWeighted(items []itemCandidate) (itemCandidate, bool) {
	if len(items) == 0 {
		return itemCandidate{}, false
	}

	totalWeight := 0
	for _, item := range items {
		weight := item.AdjustedWeight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight
	}
	if totalWeight <= 0 {
		return items[0], true
	}

	target := s.random.Intn(totalWeight)
	acc := 0
	for _, item := range items {
		weight := item.AdjustedWeight
		if weight <= 0 {
			weight = 1
		}
		acc += weight
		if target < acc {
			return item, true
		}
	}

	return items[len(items)-1], true
}

func buildItems(kind Kind, sources []string) []Item {
	items := make([]Item, 0, len(sources))
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		items = append(items, Item{
			Kind:   kind,
			Source: source,
			Weight: 1,
		})
	}

	return items
}

func filterItemsByKind(items []Item, kind Kind) []Item {
	result := make([]Item, 0)
	for _, item := range items {
		if item.Kind == kind {
			result = append(result, item)
		}
	}

	return result
}

func pruneRecent(values []time.Time, threshold time.Time) []time.Time {
	if len(values) == 0 {
		return values
	}

	index := 0
	for index < len(values) && values[index].Before(threshold) {
		index++
	}

	return append([]time.Time(nil), values[index:]...)
}

func pruneSentRecords(values []sentRecord, threshold time.Time, limit int) []sentRecord {
	if len(values) == 0 {
		return values
	}

	filtered := make([]sentRecord, 0, len(values))
	for _, value := range values {
		if value.SentAt.Before(threshold) {
			continue
		}
		filtered = append(filtered, value)
	}
	if limit > 0 && len(filtered) > limit {
		filtered = append([]sentRecord(nil), filtered[len(filtered)-limit:]...)
	}

	return filtered
}

func splitCommaSeparated(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}

	return result
}

func splitPipeSeparated(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, "|")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}

	return result
}

func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}

	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}

	return value
}

func envInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}

	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}

	return value
}

func envFloat(key string, fallback float64) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}

	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}

	return value
}

func normalizeText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer(
		"ё", "е",
		"\n", " ",
		"\t", " ",
	)
	return replacer.Replace(text)
}

func containsAny(text string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(text, token) {
			return true
		}
	}

	return false
}

func overlapCount(tags []string, messageTags map[string]struct{}) int {
	count := 0
	for _, tag := range tags {
		if _, ok := messageTags[tag]; ok {
			count++
		}
	}

	return count
}

func hasAnyTag(tags []string, expected ...string) bool {
	if len(tags) == 0 || len(expected) == 0 {
		return false
	}

	set := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		set[tag] = struct{}{}
	}
	for _, candidate := range expected {
		if _, ok := set[candidate]; ok {
			return true
		}
	}

	return false
}

func filterRepeatedCandidates(candidates []itemCandidate, history []sentRecord) []itemCandidate {
	if len(candidates) == 0 || len(history) == 0 {
		return candidates
	}

	recentSources := make(map[string]struct{}, len(history))
	recentGroups := make(map[string]struct{}, len(history))
	recentKinds := make(map[Kind]int, len(history))
	for _, record := range history {
		recentSources[record.Source] = struct{}{}
		if strings.TrimSpace(record.Group) != "" {
			recentGroups[record.Group] = struct{}{}
		}
		recentKinds[record.Kind]++
	}

	filtered := make([]itemCandidate, 0, len(candidates))
	groupFallback := make([]itemCandidate, 0, len(candidates))
	kindFallback := make([]itemCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		_, sourceSeen := recentSources[candidate.Item.Source]
		_, groupSeen := recentGroups[candidate.Item.Group]
		kindCount := recentKinds[candidate.Item.Kind]

		switch {
		case !sourceSeen && (candidate.Item.Group == "" || !groupSeen) && kindCount < 2:
			filtered = append(filtered, candidate)
		case !sourceSeen && kindCount < 2:
			groupFallback = append(groupFallback, candidate)
		case !sourceSeen:
			kindFallback = append(kindFallback, candidate)
		}
	}

	switch {
	case len(filtered) > 0:
		return filtered
	case len(groupFallback) > 0:
		return groupFallback
	case len(kindFallback) > 0:
		return kindFallback
	default:
		return candidates
	}
}

func capsRatio(text string) float64 {
	var total int
	var uppercase int

	for _, r := range text {
		if r >= 'A' && r <= 'Z' {
			total++
			uppercase++
			continue
		}
		if r >= 'a' && r <= 'z' {
			total++
			continue
		}
		if r >= 'А' && r <= 'Я' {
			total++
			uppercase++
			continue
		}
		if r >= 'а' && r <= 'я' {
			total++
		}
	}

	if total == 0 {
		return 0
	}

	return float64(uppercase) / float64(total)
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}

	return b
}

func normalizedWeight(weight int) int {
	if weight <= 0 {
		return 1
	}

	return weight
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

var laughTokens = []string{
	"ахах",
	"аха",
	"ор",
	"ору",
	"лол",
	"ржу",
	"угар",
	"кек",
	"хаха",
	"xd",
	"lmao",
}

var hypeTokens = []string{
	"жесть",
	"имба",
	"кринж",
	"мем",
	"мемчик",
	"капец",
	"сильно",
	"разнос",
	"легенда",
	"шок",
}

var soundTokens = []string{
	"sound",
	"sounds",
	"звук",
	"звуки",
	"саунд",
	"музыка",
	"трек",
	"голосовуха",
}
