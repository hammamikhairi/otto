package timer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
	"github.com/hammamikhairi/ottocook/internal/storage"
)

// mockNotifier collects notifications for testing.
type mockNotifier struct {
	mu       sync.Mutex
	messages []string
	urgent   []string
}

func (m *mockNotifier) Notify(_ context.Context, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

func (m *mockNotifier) NotifyUrgent(_ context.Context, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.urgent = append(m.urgent, msg)
	return nil
}

func (m *mockNotifier) urgentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.urgent)
}

func TestSupervisorFiresTimer(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := storage.NewMemoryStore(log)
	notifier := &mockNotifier{}
	ctx := context.Background()

	// Create a session with a timer that's about to fire.
	session := &domain.Session{
		ID:               "timer-test",
		RecipeID:         "test",
		RecipeName:       "Test",
		Status:           domain.SessionActive,
		CurrentStepIndex: 0,
		StepStates:       map[int]*domain.StepState{0: {Status: domain.StepActive}},
		TimerStates: map[string]*domain.TimerState{
			"t1": {
				ID:        "t1",
				StepID:    "step-1",
				Label:     "Test Timer",
				Duration:  2 * time.Second,
				Remaining: 100 * time.Millisecond, // About to fire.
				Status:    domain.TimerRunning,
			},
		},
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Create supervisor with fast tick.
	sup := New(store, notifier, log, WithTickInterval(50*time.Millisecond), WithNotifyCooldown(100*time.Millisecond))
	sup.Start(ctx)
	defer sup.Stop()

	// Wait for the timer to fire.
	time.Sleep(300 * time.Millisecond)

	if notifier.urgentCount() == 0 {
		t.Fatal("expected at least one urgent notification for fired timer")
	}

	// Verify the timer state changed.
	s, err := store.Load(ctx, "timer-test")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ts := s.TimerStates["t1"]
	if ts.Status != domain.TimerFired {
		t.Fatalf("expected timer status Fired, got %s", ts.Status)
	}
}

func TestSupervisorRespectsMaxEscalation(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := storage.NewMemoryStore(log)
	notifier := &mockNotifier{}
	ctx := context.Background()

	session := &domain.Session{
		ID:               "escalation-test",
		RecipeID:         "test",
		RecipeName:       "Test",
		Status:           domain.SessionActive,
		CurrentStepIndex: 0,
		StepStates:       map[int]*domain.StepState{0: {Status: domain.StepActive}},
		TimerStates: map[string]*domain.TimerState{
			"t1": {
				ID:              "t1",
				StepID:          "step-1",
				Label:           "Test Timer",
				Duration:        1 * time.Second,
				Remaining:       0,
				Status:          domain.TimerFired,
				EscalationLevel: 10, // Past max.
				LastNotified:    time.Now().Add(-1 * time.Hour),
			},
		},
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	sup := New(store, notifier, log,
		WithTickInterval(50*time.Millisecond),
		WithMaxEscalation(3),
	)
	sup.Start(ctx)
	defer sup.Stop()

	time.Sleep(200 * time.Millisecond)

	// Should NOT have sent any notification since escalation is past max.
	notifier.mu.Lock()
	total := len(notifier.messages) + len(notifier.urgent)
	notifier.mu.Unlock()

	if total > 0 {
		t.Fatalf("expected no notifications past max escalation, got %d", total)
	}
}

func TestSupervisorSkipsPausedSessions(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := storage.NewMemoryStore(log)
	notifier := &mockNotifier{}
	ctx := context.Background()

	session := &domain.Session{
		ID:               "paused-test",
		RecipeID:         "test",
		RecipeName:       "Test",
		Status:           domain.SessionPaused,
		CurrentStepIndex: 0,
		StepStates:       map[int]*domain.StepState{0: {Status: domain.StepActive}},
		TimerStates: map[string]*domain.TimerState{
			"t1": {
				ID:        "t1",
				StepID:    "step-1",
				Label:     "Test Timer",
				Duration:  1 * time.Second,
				Remaining: 50 * time.Millisecond,
				Status:    domain.TimerRunning, // Running but session is paused.
			},
		},
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	sup := New(store, notifier, log, WithTickInterval(50*time.Millisecond))
	sup.Start(ctx)
	defer sup.Stop()

	time.Sleep(200 * time.Millisecond)

	// Paused sessions should be skipped -- no notifications.
	if notifier.urgentCount() > 0 {
		t.Fatal("expected no notifications for paused session")
	}
}
