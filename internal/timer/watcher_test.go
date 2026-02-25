package timer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
	"github.com/hammamikhairi/ottocook/internal/recipe"
	"github.com/hammamikhairi/ottocook/internal/storage"
)

// collectingNotifier captures messages for assertions.
type collectingNotifier struct {
	mu       sync.Mutex
	messages []string
}

func (n *collectingNotifier) Notify(_ context.Context, msg string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, msg)
	return nil
}

func (n *collectingNotifier) NotifyUrgent(_ context.Context, msg string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, msg)
	return nil
}

func (n *collectingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.messages)
}

func (n *collectingNotifier) last() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.messages) == 0 {
		return ""
	}
	return n.messages[len(n.messages)-1]
}

func TestWatcherPausedSessionNudge(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := storage.NewMemoryStore(log)
	recipes := recipe.NewMemorySource(log)
	notifier := &collectingNotifier{}
	ctx := context.Background()

	session := &domain.Session{
		ID:               "watcher-paused",
		RecipeID:         "vegetable-stir-fry",
		RecipeName:       "Vegetable Stir Fry",
		Status:           domain.SessionPaused,
		CurrentStepIndex: 0,
		Servings:         2,
		StepStates: map[int]*domain.StepState{
			0: {Status: domain.StepActive, StartedAt: time.Now().Add(-2 * time.Minute)},
			1: {Status: domain.StepPending},
			2: {Status: domain.StepPending},
			3: {Status: domain.StepPending},
			4: {Status: domain.StepPending},
			5: {Status: domain.StepPending},
			6: {Status: domain.StepPending},
			7: {Status: domain.StepPending},
		},
		TimerStates: map[string]*domain.TimerState{},
		StartedAt:   time.Now().Add(-5 * time.Minute),
		UpdatedAt:   time.Now().Add(-3 * time.Minute), // Paused 3 min ago.
	}

	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	w := NewWatcher(store, recipes, notifier, log, WithWatchInterval(50*time.Millisecond))
	wCtx, cancel := context.WithCancel(ctx)
	go w.Run(wCtx)

	time.Sleep(200 * time.Millisecond)
	cancel()

	if notifier.count() == 0 {
		t.Fatal("expected watcher to nudge about paused session")
	}

	msg := notifier.last()
	if msg == "" {
		t.Fatal("expected a message")
	}
	t.Logf("watcher said: %s", msg)
}

func TestWatcherFiredTimerAlert(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := storage.NewMemoryStore(log)
	recipes := recipe.NewMemorySource(log)
	notifier := &collectingNotifier{}
	ctx := context.Background()

	session := &domain.Session{
		ID:               "watcher-fired",
		RecipeID:         "chicken-alfredo",
		RecipeName:       "Chicken Alfredo",
		Status:           domain.SessionActive,
		CurrentStepIndex: 0,
		Servings:         2,
		StepStates: map[int]*domain.StepState{
			0: {Status: domain.StepActive, StartedAt: time.Now().Add(-10 * time.Minute)},
			1: {Status: domain.StepPending},
			2: {Status: domain.StepPending},
			3: {Status: domain.StepPending},
			4: {Status: domain.StepPending},
			5: {Status: domain.StepPending},
			6: {Status: domain.StepPending},
			7: {Status: domain.StepPending},
		},
		TimerStates: map[string]*domain.TimerState{
			"t1": {
				ID:        "t1",
				StepID:    "ca-1",
				Label:     "Water boiling",
				Duration:  8 * time.Minute,
				Remaining: 0,
				Status:    domain.TimerFired,
			},
		},
		StartedAt: time.Now().Add(-10 * time.Minute),
		UpdatedAt: time.Now(),
	}

	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	w := NewWatcher(store, recipes, notifier, log, WithWatchInterval(50*time.Millisecond))
	wCtx, cancel := context.WithCancel(ctx)
	go w.Run(wCtx)

	time.Sleep(200 * time.Millisecond)
	cancel()

	if notifier.count() == 0 {
		t.Fatal("expected watcher to alert about fired timer")
	}

	msg := notifier.last()
	if msg == "" {
		t.Fatal("expected a message")
	}
	t.Logf("watcher said: %s", msg)
}

func TestWatcherOverdueStep(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := storage.NewMemoryStore(log)
	recipes := recipe.NewMemorySource(log)
	notifier := &collectingNotifier{}
	ctx := context.Background()

	// Chicken alfredo step 3 (index 2) has Duration=12m. We'll set StartedAt 25 minutes ago (>2x).
	session := &domain.Session{
		ID:               "watcher-overdue",
		RecipeID:         "chicken-alfredo",
		RecipeName:       "Chicken Alfredo",
		Status:           domain.SessionActive,
		CurrentStepIndex: 2,
		Servings:         2,
		StepStates: map[int]*domain.StepState{
			0: {Status: domain.StepDone},
			1: {Status: domain.StepDone},
			2: {Status: domain.StepActive, StartedAt: time.Now().Add(-25 * time.Minute)},
			3: {Status: domain.StepPending},
			4: {Status: domain.StepPending},
			5: {Status: domain.StepPending},
			6: {Status: domain.StepPending},
			7: {Status: domain.StepPending},
		},
		TimerStates: map[string]*domain.TimerState{},
		StartedAt:   time.Now().Add(-12 * time.Minute),
		UpdatedAt:   time.Now(),
	}

	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	w := NewWatcher(store, recipes, notifier, log, WithWatchInterval(50*time.Millisecond))
	wCtx, cancel := context.WithCancel(ctx)
	go w.Run(wCtx)

	time.Sleep(200 * time.Millisecond)
	cancel()

	if notifier.count() == 0 {
		t.Fatal("expected watcher to nudge about overdue step")
	}

	msg := notifier.last()
	if msg == "" {
		t.Fatal("expected a message")
	}
	t.Logf("watcher said: %s", msg)
}

func TestWatcherQuietWhenNothingToReport(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	store := storage.NewMemoryStore(log)
	recipes := recipe.NewMemorySource(log)
	notifier := &collectingNotifier{}
	ctx := context.Background()

	// Active session, just started, no timers, manual step. Should be quiet.
	session := &domain.Session{
		ID:               "watcher-quiet",
		RecipeID:         "scrambled-eggs",
		RecipeName:       "Scrambled Eggs",
		Status:           domain.SessionActive,
		CurrentStepIndex: 0,
		Servings:         1,
		StepStates: map[int]*domain.StepState{
			0: {Status: domain.StepActive, StartedAt: time.Now()},
			1: {Status: domain.StepPending},
			2: {Status: domain.StepPending},
			3: {Status: domain.StepPending},
		},
		TimerStates: map[string]*domain.TimerState{},
		StartedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	w := NewWatcher(store, recipes, notifier, log, WithWatchInterval(50*time.Millisecond))
	wCtx, cancel := context.WithCancel(ctx)
	go w.Run(wCtx)

	time.Sleep(200 * time.Millisecond)
	cancel()

	if notifier.count() > 0 {
		t.Fatalf("expected no notifications for fresh session, got %d: %q", notifier.count(), notifier.last())
	}
}
