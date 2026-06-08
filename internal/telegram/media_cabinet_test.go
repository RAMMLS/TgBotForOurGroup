package telegram

import (
	"testing"

	telebot "gopkg.in/telebot.v3"
)

func TestExtractMediaCallbackData(t *testing.T) {
	t.Run("parsed callback", func(t *testing.T) {
		data, ok := extractMediaCallbackData(&telebot.Callback{
			Unique: mediaCallbackUnique,
			Data:   "importpack",
		})
		if !ok {
			t.Fatal("expected parsed callback to be handled")
		}
		if data != "importpack" {
			t.Fatalf("expected importpack, got %q", data)
		}
	})

	t.Run("raw callback payload", func(t *testing.T) {
		data, ok := extractMediaCallbackData(&telebot.Callback{
			Data: "\f" + mediaCallbackUnique + "|importnew",
		})
		if !ok {
			t.Fatal("expected raw callback payload to be handled")
		}
		if data != "importnew" {
			t.Fatalf("expected importnew, got %q", data)
		}
	})
}
