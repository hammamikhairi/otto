package engine

import (
	"crypto/rand"
	"fmt"
)

// generateID creates a short random hex ID for sessions.
func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback -- should never happen.
		return fmt.Sprintf("sess-%d", b)
	}
	return fmt.Sprintf("%x", b)
}
