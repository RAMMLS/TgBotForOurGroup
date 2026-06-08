package presence

import (
	"fmt"
	"strings"
	"time"
)

func BuildActiveVoiceStatusMessage(channelName string, startedAt time.Time, participants []string) string {
	var builder strings.Builder

	builder.WriteString("🎙 В голосовом канале Discord кто-то сидит\n")
	builder.WriteString(fmt.Sprintf("Канал: %s\n", channelName))
	builder.WriteString(fmt.Sprintf("⏱ В чате: %s\n", FormatDuration(time.Since(startedAt))))
	builder.WriteString(fmt.Sprintf("👥 Участники (%d):\n", len(participants)))

	for _, participant := range participants {
		builder.WriteString("- ")
		builder.WriteString(participant)
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String())
}

func BuildClosedVoiceStatusMessage(channelName string, startedAt time.Time) string {
	return fmt.Sprintf(
		"🔇 Голосовой канал Discord опустел\nКанал: %s\n⏱ Провели в чате: %s",
		channelName,
		FormatDuration(time.Since(startedAt)),
	)
}

func FormatDuration(duration time.Duration) string {
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
