package domain

import "context"

// RecipeSource provides recipes. Implementations can be in-memory (hardcoded),
// file-based, API-backed, or LLM-generated.
type RecipeSource interface {
	List(ctx context.Context) ([]RecipeSummary, error)
	Get(ctx context.Context, id string) (*Recipe, error)
	Search(ctx context.Context, query string) ([]RecipeSummary, error)
}

// SessionStore persists cooking sessions. Implementations can be in-memory,
// SQLite, Supabase, or any other backend.
type SessionStore interface {
	Save(ctx context.Context, session *Session) error
	Load(ctx context.Context, id string) (*Session, error)
	Delete(ctx context.Context, id string) error
	ListActive(ctx context.Context) ([]*Session, error)
}

// IntentParser converts raw user input into structured intents.
// Implementations can be keyword-based, regex, or LLM-powered.
type IntentParser interface {
	Parse(ctx context.Context, input string, session *Session) (*Intent, error)
}

// Notifier delivers messages to the user. Implementations can write to
// stdout, push notifications, or use text-to-speech.
type Notifier interface {
	Notify(ctx context.Context, message string) error
	NotifyUrgent(ctx context.Context, message string) error
}

// SpeechProvider handles voice input/output. The Listen method is for
// speech-to-text (future), and Speak sends text through the TTS pipeline.
// The no-op implementation is used when voice is disabled.
type SpeechProvider interface {
	Listen(ctx context.Context) (string, error)
	Speak(ctx context.Context, text string) error
}
