package linking

import "strings"

type LinkedChat struct {
	ChatID int64
	Label  string
}

func BuildStartScreenText(prefix string, linkedChats []LinkedChat) string {
	var text strings.Builder
	if prefix != "" {
		text.WriteString(prefix)
		text.WriteString("\n\n")
	}

	if len(linkedChats) == 0 {
		text.WriteString("Сейчас у тебя нет активных привязок. Открой Deep Link из Discord, чтобы связать аккаунт.")
		return text.String()
	}

	text.WriteString("Текущие привязки:\n")
	for _, chat := range linkedChats {
		text.WriteString("• ")
		text.WriteString(chat.Label)
		text.WriteString("\n")
	}
	text.WriteString("\nНажми на кнопку ниже, чтобы отвязать себя от нужной беседы.")

	return text.String()
}

func TruncateButtonLabel(label string) string {
	const maxLen = 64
	if len(label) <= maxLen {
		return label
	}

	return label[:maxLen]
}
