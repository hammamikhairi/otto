package timer

import (
	"context"
	"fmt"
	"time"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// WatcherOption configures the watcher.
type WatcherOption func(*Watcher)

// WithWatchInterval sets how often the watcher checks session state.
func WithWatchInterval(d time.Duration) WatcherOption {
	return func(w *Watcher) {
		w.interval = d
	}
}

// Watcher periodically inspects the full session state and provides
// contextual commentary — reminders about idle steps, timer awareness,
// and general "keep an eye on it" nudges. Runs on a slower cycle than
// the timer supervisor (default: 1 minute).
type Watcher struct {
	store    domain.SessionStore
	recipes  domain.RecipeSource
	notifier domain.Notifier
	log      *logger.Logger
	interval time.Duration
}

// NewWatcher creates a watcher with the given dependencies.
func NewWatcher(store domain.SessionStore, recipes domain.RecipeSource, notifier domain.Notifier, log *logger.Logger, opts ...WatcherOption) *Watcher {
	w := &Watcher{
		store:    store,
		recipes:  recipes,
		notifier: notifier,
		log:      log,
		interval: 1 * time.Minute,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Run starts the watcher loop. Blocks until ctx is cancelled.
// Intended to be called as a goroutine.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.log.Info("watcher started (interval=%s)", w.interval)

	for {
		select {
		case <-ctx.Done():
			w.log.Info("watcher stopped")
			return
		case <-ticker.C:
			w.check(ctx)
		}
	}
}

// check runs one watcher cycle across all active sessions.
func (w *Watcher) check(ctx context.Context) {
	sessions, err := w.store.ListActive(ctx)
	if err != nil {
		w.log.Error("watcher: listing active sessions: %v", err)
		return
	}

	for _, session := range sessions {
		w.inspect(ctx, session)
	}
}

// inspect examines a single session and decides what to say.
func (w *Watcher) inspect(ctx context.Context, session *domain.Session) {
	now := time.Now()

	// Log the check itself.
	w.log.Debug("watcher: checked status — session=%s recipe=%s status=%s step=%d/%d",
		session.ID[:8], session.RecipeName, session.Status,
		session.CurrentStepIndex+1, len(session.StepStates))

	// Log all timer states.
	for _, ts := range session.TimerStates {
		w.log.Debug("watcher: timer %s (%s) — status=%s remaining=%s escalation=%d",
			ts.ID, ts.Label, ts.Status, ts.Remaining.Round(time.Second), ts.EscalationLevel)
	}

	// Get the recipe for step context.
	recipe, err := w.recipes.Get(ctx, session.RecipeID)
	if err != nil {
		w.log.Error("watcher: loading recipe %s: %v", session.RecipeID, err)
		return
	}

	idx := session.CurrentStepIndex
	if idx >= len(recipe.Steps) {
		return
	}
	step := &recipe.Steps[idx]
	stepState := session.StepStates[idx]

	// How long has the user been on this step?
	onStepFor := time.Duration(0)
	if !stepState.StartedAt.IsZero() {
		onStepFor = now.Sub(stepState.StartedAt)
	}

	// Build a contextual message based on what we see.
	msg := w.buildMessage(session, step, stepState, onStepFor)
	if msg == "" {
		return
	}

	if err := w.notifier.Notify(ctx, msg); err != nil {
		w.log.Error("watcher: notify: %v", err)
	}
}

// buildMessage decides what to tell the user based on current state.
func (w *Watcher) buildMessage(session *domain.Session, step *domain.Step, stepState *domain.StepState, onStepFor time.Duration) string {
	// Paused session — gentle nudge.
	if session.Status == domain.SessionPaused {
		elapsed := time.Since(session.UpdatedAt).Round(time.Second)
		return fmt.Sprintf("[Watcher] Session paused for %s. Your food isn't cooking itself.", elapsed)
	}

	// Collect active timer info.
	var runningTimers []string
	var firedTimers []string
	for _, ts := range session.TimerStates {
		switch ts.Status {
		case domain.TimerRunning:
			runningTimers = append(runningTimers, fmt.Sprintf("%s (%s left)", ts.Label, ts.Remaining.Round(time.Second)))
		case domain.TimerFired:
			firedTimers = append(firedTimers, ts.Label)
		}
	}

	// Fired timers take priority — something needs attention.
	if len(firedTimers) > 0 {
		return fmt.Sprintf("[Watcher] Heads up — %s fired and waiting on you.", joinNames(firedTimers))
	}

	// Step has an expected duration and user is way over it.
	if step.Duration > 0 && onStepFor > step.Duration*2 {
		msg := fmt.Sprintf("[Watcher] You've been on step %d for %s (expected ~%s). Everything okay?",
			step.Order, onStepFor.Round(time.Second), step.Duration.Round(time.Second))
		if len(runningTimers) > 0 {
			msg += fmt.Sprintf(" Active timers: %s.", joinNames(extractNames(runningTimers)))
		}
		return msg
	}

	// Step has no duration but user has been on it a while (>3 min for manual steps).
	if step.Duration == 0 && onStepFor > 3*time.Minute {
		return fmt.Sprintf("[Watcher] Still on step %d (%s). Take your time, but don't forget about it.",
			step.Order, onStepFor.Round(time.Second))
	}

	// Timed step, user is within expected range — just log active timers.
	if len(runningTimers) > 0 {
		w.log.Debug("watcher: active timers for session %s: %v", session.ID[:8], runningTimers)
	}

	// Nothing interesting to report.
	w.log.Debug("watcher: session %s — step %d, on it for %s, nothing to report",
		session.ID[:8], step.Order, onStepFor.Round(time.Second))

	return ""
}

// joinNames joins a slice of names into a comma-separated string.
func joinNames(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	result := ""
	for i, n := range names {
		if i == len(names)-1 {
			result += " and " + n
		} else if i > 0 {
			result += ", " + n
		} else {
			result = n
		}
	}
	return result
}

// extractNames strips the parenthetical from timer descriptions like "Label (3m left)".
func extractNames(timerDescs []string) []string {
	out := make([]string, len(timerDescs))
	for i, desc := range timerDescs {
		// Just take everything — the full description is useful.
		out[i] = desc
	}
	return out
}
