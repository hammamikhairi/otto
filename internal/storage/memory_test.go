package storage

import (
	"context"
	"testing"
	"time"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

func TestMemoryStoreCRUD(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := NewMemoryStore(log)
	ctx := context.Background()

	session := &domain.Session{
		ID:               "test-session-1",
		RecipeID:         "test-recipe",
		RecipeName:       "Test Recipe",
		Status:           domain.SessionActive,
		CurrentStepIndex: 0,
		StepStates:       make(map[int]*domain.StepState),
		TimerStates:      make(map[string]*domain.TimerState),
		StartedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	// Save.
	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load.
	loaded, err := store.Load(ctx, "test-session-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ID != session.ID {
		t.Fatalf("expected ID %s, got %s", session.ID, loaded.ID)
	}

	// Load nonexistent.
	_, err = store.Load(ctx, "nonexistent")
	if err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// ListActive.
	active, err := store.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active session, got %d", len(active))
	}

	// Delete.
	if err := store.Delete(ctx, "test-session-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = store.Load(ctx, "test-session-1")
	if err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete nonexistent.
	if err := store.Delete(ctx, "nonexistent"); err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemoryStoreListActiveFilters(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := NewMemoryStore(log)
	ctx := context.Background()

	sessions := []*domain.Session{
		{ID: "s1", Status: domain.SessionActive, StepStates: map[int]*domain.StepState{}, TimerStates: map[string]*domain.TimerState{}},
		{ID: "s2", Status: domain.SessionPaused, StepStates: map[int]*domain.StepState{}, TimerStates: map[string]*domain.TimerState{}},
		{ID: "s3", Status: domain.SessionCompleted, StepStates: map[int]*domain.StepState{}, TimerStates: map[string]*domain.TimerState{}},
		{ID: "s4", Status: domain.SessionAbandoned, StepStates: map[int]*domain.StepState{}, TimerStates: map[string]*domain.TimerState{}},
	}

	for _, s := range sessions {
		if err := store.Save(ctx, s); err != nil {
			t.Fatalf("save %s: %v", s.ID, err)
		}
	}

	active, err := store.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("expected 2 active/paused sessions, got %d", len(active))
	}
}
