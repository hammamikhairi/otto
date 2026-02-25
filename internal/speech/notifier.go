package speech

import (
	"context"
	"regexp"
	"strings"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Compile-time interface check.
var _ domain.Notifier = (*SpeakingNotifier)(nil)

// SpeakingNotifier wraps a text notifier and also speaks messages through the Mouth.
// Messages are printed immediately (via the inner notifier) and queued for speech.
type SpeakingNotifier struct {
	text  domain.Notifier
	mouth *Mouth
	log   *logger.Logger
}

// NewSpeakingNotifier creates a notifier that both prints and speaks.
func NewSpeakingNotifier(text domain.Notifier, mouth *Mouth, log *logger.Logger) *SpeakingNotifier {
	return &SpeakingNotifier{
		text:  text,
		mouth: mouth,
		log:   log,
	}
}

// Notify prints the message and queues it for speech at normal priority.
func (n *SpeakingNotifier) Notify(ctx context.Context, message string) error {
	if err := n.text.Notify(ctx, message); err != nil {
		return err
	}
	n.mouth.Say(cleanForSpeech(message), PriorityNormal)
	return nil
}

// NotifyUrgent prints the message and queues it for speech at high priority.
func (n *SpeakingNotifier) NotifyUrgent(ctx context.Context, message string) error {
	if err := n.text.NotifyUrgent(ctx, message); err != nil {
		return err
	}
	n.mouth.Say(cleanForSpeech(message), PriorityHigh)
	return nil
}

// cleanForSpeech strips formatting artifacts that shouldn't be spoken.
var bracketPrefix = regexp.MustCompile(`^\[[A-Za-z]+\]\s*`)
var ansiCodes = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func cleanForSpeech(msg string) string {
	cleaned := ansiCodes.ReplaceAllString(msg, "")
	cleaned = bracketPrefix.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(cleaned)
	return cleaned
}
