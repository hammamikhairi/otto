// Package speech provides speech-to-text and text-to-speech implementations.
package speech

import (
	"context"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Compile-time interface check.
var _ domain.SpeechProvider = (*NoOp)(nil)

// NoOp is a speech provider that does nothing. Used when voice is disabled.
type NoOp struct {
	log *logger.Logger
}

// NewNoOp creates a no-op speech provider.
func NewNoOp(log *logger.Logger) *NoOp {
	return &NoOp{log: log}
}

// Listen returns ErrNotImplemented. Replace with a real STT provider when ready.
func (n *NoOp) Listen(ctx context.Context) (string, error) {
	return "", domain.ErrNotImplemented
}

// Speak does nothing. Replace with a real TTS provider when ready.
func (n *NoOp) Speak(ctx context.Context, text string) error {
	n.log.Debug("speech no-op: would say %q", text)
	return nil
}
