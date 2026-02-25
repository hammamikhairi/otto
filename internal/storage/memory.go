// Package storage provides session persistence implementations.
package storage

import (
	"context"
	"sync"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Compile-time interface check.
var _ domain.SessionStore = (*MemoryStore)(nil)

// MemoryStore is an in-memory session store. Safe for concurrent access.
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*domain.Session
	log      *logger.Logger
}

// NewMemoryStore creates an empty in-memory session store.
func NewMemoryStore(log *logger.Logger) *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]*domain.Session),
		log:      log,
	}
}

// Save persists a session. Overwrites if it already exists.
func (s *MemoryStore) Save(ctx context.Context, session *domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Debug("saving session %s (recipe=%s, status=%s)", session.ID, session.RecipeID, session.Status)
	s.sessions[session.ID] = session
	return nil
}

// Load retrieves a session by ID.
func (s *MemoryStore) Load(ctx context.Context, id string) (*domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[id]
	if !ok {
		s.log.Debug("session not found: %s", id)
		return nil, domain.ErrNotFound
	}
	return sess, nil
}

// Delete removes a session by ID.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[id]; !ok {
		return domain.ErrNotFound
	}
	delete(s.sessions, id)
	s.log.Debug("deleted session %s", id)
	return nil
}

// ListActive returns all sessions with active or paused status.
func (s *MemoryStore) ListActive(ctx context.Context) ([]*domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*domain.Session
	for _, sess := range s.sessions {
		if sess.Status == domain.SessionActive || sess.Status == domain.SessionPaused {
			out = append(out, sess)
		}
	}
	s.log.Debug("listing active sessions, count=%d", len(out))
	return out, nil
}
