package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
	"github.com/hammamikhairi/ottocook/internal/recipe"
	"github.com/hammamikhairi/ottocook/internal/storage"
)

func setupEngine(t *testing.T) (*Engine, context.Context) {
	t.Helper()
	log := logger.New(logger.LevelOff, nil)
	recipes := recipe.NewMemorySource(log)
	store := storage.NewMemoryStore(log)
	eng := New(recipes, store, log)
	return eng, context.Background()
}

func TestStartSession(t *testing.T) {
	eng, ctx := setupEngine(t)

	tests := []struct {
		name     string
		recipeID string
		servings int
		wantErr  bool
	}{
		{"valid recipe", "chicken-alfredo", 2, false},
		{"valid recipe default servings", "vegetable-stir-fry", 0, false},
		{"unknown recipe", "nonexistent", 2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := eng.StartSession(ctx, tt.recipeID, tt.servings)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if session.ID == "" {
				t.Fatal("session ID is empty")
			}
			if session.Status != domain.SessionActive {
				t.Fatalf("expected active status, got %s", session.Status)
			}
			if session.CurrentStepIndex != 0 {
				t.Fatalf("expected step index 0, got %d", session.CurrentStepIndex)
			}
			if session.StepStates[0].Status != domain.StepActive {
				t.Fatalf("expected first step active, got %s", session.StepStates[0].Status)
			}
		})
	}
}

func TestAdvanceSteps(t *testing.T) {
	eng, ctx := setupEngine(t)

	session, err := eng.StartSession(ctx, "vegetable-stir-fry", 2)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Vegetable stir fry has 8 steps. Advance through all of them.
	for i := 0; i < 7; i++ {
		step, err := eng.Advance(ctx, session.ID)
		if err != nil {
			t.Fatalf("advance step %d: %v", i+2, err)
		}
		if step.Order != i+2 {
			t.Fatalf("expected step order %d, got %d", i+2, step.Order)
		}
	}

	// One more advance should complete the session.
	_, err = eng.Advance(ctx, session.ID)
	if !errors.Is(err, domain.ErrNoMoreSteps) {
		t.Fatalf("expected ErrNoMoreSteps, got %v", err)
	}

	// Verify session is completed.
	s, err := eng.Status(ctx, session.ID)
	if err != nil {
		t.Fatalf("getting status: %v", err)
	}
	if s.Status != domain.SessionCompleted {
		t.Fatalf("expected completed, got %s", s.Status)
	}
}

func TestSkip(t *testing.T) {
	eng, ctx := setupEngine(t)

	session, err := eng.StartSession(ctx, "vegetable-stir-fry", 2)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Skip first step.
	step, err := eng.Skip(ctx, session.ID)
	if err != nil {
		t.Fatalf("skip: %v", err)
	}
	if step.Order != 2 {
		t.Fatalf("expected step 2 after skip, got %d", step.Order)
	}

	// Verify the first step was marked skipped.
	s, err := eng.Status(ctx, session.ID)
	if err != nil {
		t.Fatalf("getting status: %v", err)
	}
	if s.StepStates[0].Status != domain.StepSkipped {
		t.Fatalf("expected skipped, got %s", s.StepStates[0].Status)
	}
}

func TestPauseResume(t *testing.T) {
	eng, ctx := setupEngine(t)

	session, err := eng.StartSession(ctx, "chicken-alfredo", 2)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Pause.
	if err := eng.Pause(ctx, session.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	s, _ := eng.Status(ctx, session.ID)
	if s.Status != domain.SessionPaused {
		t.Fatalf("expected paused, got %s", s.Status)
	}

	// Can't advance while paused.
	_, err = eng.Advance(ctx, session.ID)
	if !errors.Is(err, domain.ErrSessionNotActive) {
		t.Fatalf("expected ErrSessionNotActive, got %v", err)
	}

	// Resume.
	resumed, err := eng.Resume(ctx, session.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Status != domain.SessionActive {
		t.Fatalf("expected active after resume, got %s", resumed.Status)
	}

	// Can advance again.
	_, err = eng.Advance(ctx, session.ID)
	if err != nil {
		t.Fatalf("advance after resume: %v", err)
	}
}

func TestAbandon(t *testing.T) {
	eng, ctx := setupEngine(t)

	session, err := eng.StartSession(ctx, "vegetable-stir-fry", 2)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	if err := eng.Abandon(ctx, session.ID); err != nil {
		t.Fatalf("abandon: %v", err)
	}

	s, _ := eng.Status(ctx, session.ID)
	if s.Status != domain.SessionAbandoned {
		t.Fatalf("expected abandoned, got %s", s.Status)
	}
}

func TestTimerStartsOnStep(t *testing.T) {
	eng, ctx := setupEngine(t)

	// Chicken alfredo step 1 has a timer.
	session, err := eng.StartSession(ctx, "chicken-alfredo", 2)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	if len(session.TimerStates) == 0 {
		t.Fatal("expected timer to be created for first step")
	}

	// Timer should start as pending (user must confirm).
	for _, ts := range session.TimerStates {
		if ts.Label == "Water boiling" && ts.Status != domain.TimerPending {
			t.Fatalf("expected 'Water boiling' timer to be pending, got %s", ts.Status)
		}
	}

	// User confirms — timer becomes running.
	n, err := eng.StartPendingTimers(ctx, session.ID)
	if err != nil {
		t.Fatalf("start pending timers: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 timer started, got %d", n)
	}

	s, _ := eng.Status(ctx, session.ID)
	for _, ts := range s.TimerStates {
		if ts.Label == "Water boiling" && ts.Status != domain.TimerRunning {
			t.Fatalf("expected 'Water boiling' timer to be running after confirm, got %s", ts.Status)
		}
	}
}

func TestTimerKeepsRunningOnAdvance(t *testing.T) {
	eng, ctx := setupEngine(t)

	session, err := eng.StartSession(ctx, "chicken-alfredo", 2)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Start the pending timer, then advance.
	eng.StartPendingTimers(ctx, session.ID)

	_, err = eng.Advance(ctx, session.ID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}

	// Timer for step ca-1 should still be running (not dismissed).
	s, _ := eng.Status(ctx, session.ID)
	for _, ts := range s.TimerStates {
		if ts.StepID == "ca-1" && ts.Status != domain.TimerRunning {
			t.Fatalf("expected timer for step ca-1 to still be running, got %s", ts.Status)
		}
	}
}

func TestPauseResumesTimers(t *testing.T) {
	eng, ctx := setupEngine(t)

	session, err := eng.StartSession(ctx, "chicken-alfredo", 2)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Start the pending timer first.
	eng.StartPendingTimers(ctx, session.ID)

	// Pause should pause running timers.
	if err := eng.Pause(ctx, session.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	s, _ := eng.Status(ctx, session.ID)
	for _, ts := range s.TimerStates {
		if ts.Status == domain.TimerRunning {
			t.Fatalf("expected timer %s to be paused, got running", ts.ID)
		}
	}

	// Resume should resume timers.
	_, err = eng.Resume(ctx, session.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	s, _ = eng.Status(ctx, session.ID)
	for _, ts := range s.TimerStates {
		if ts.StepID == "ca-1" && ts.Status != domain.TimerRunning {
			t.Fatalf("expected timer %s to be running after resume, got %s", ts.ID, ts.Status)
		}
	}
}
