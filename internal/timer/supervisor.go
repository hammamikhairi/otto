// Package timer implements the background timer supervisor that monitors
// active cooking sessions and fires notifications when timers expire.
package timer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Option configures the supervisor.
type Option func(*Supervisor)

// WithTickInterval sets how often the supervisor checks timers.
func WithTickInterval(d time.Duration) Option {
	return func(s *Supervisor) {
		s.tickInterval = d
	}
}

// WithNotifyCooldown sets the minimum time between repeated notifications.
func WithNotifyCooldown(d time.Duration) Option {
	return func(s *Supervisor) {
		s.notifyCooldown = d
	}
}

// WithMaxEscalation sets the escalation level after which the supervisor stops nagging.
func WithMaxEscalation(level int) Option {
	return func(s *Supervisor) {
		s.maxEscalation = level
	}
}

// WithReminderInterval sets how often running timers send periodic reminders.
func WithReminderInterval(d time.Duration) Option {
	return func(s *Supervisor) {
		s.reminderInterval = d
	}
}

// WithAlmostDoneThreshold sets how close to expiry a timer must be to
// trigger the "almost done" warning.
func WithAlmostDoneThreshold(d time.Duration) Option {
	return func(s *Supervisor) {
		s.almostDoneThreshold = d
	}
}

// WithWatcher enables the session watcher with the given recipe source and options.
func WithWatcher(recipes domain.RecipeSource, opts ...WatcherOption) Option {
	return func(s *Supervisor) {
		s.watcherRecipes = recipes
		s.watcherOpts = opts
	}
}

// Supervisor runs in the background and manages timer countdown + notifications.
// Optionally runs a Watcher on a slower cycle for contextual session awareness.
type Supervisor struct {
	store               domain.SessionStore
	notifier            domain.Notifier
	log                 *logger.Logger
	tickInterval        time.Duration
	notifyCooldown      time.Duration
	maxEscalation       int
	reminderInterval    time.Duration // periodic "X remaining" reminders
	almostDoneThreshold time.Duration // "almost done" warning threshold

	watcherRecipes domain.RecipeSource
	watcherOpts    []WatcherOption
	watcher        *Watcher

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
}

// New creates a timer supervisor with the given dependencies and options.
func New(store domain.SessionStore, notifier domain.Notifier, log *logger.Logger, opts ...Option) *Supervisor {
	s := &Supervisor{
		store:               store,
		notifier:            notifier,
		log:                 log,
		tickInterval:        1 * time.Second,
		notifyCooldown:      15 * time.Second,
		maxEscalation:       3,
		reminderInterval:    2 * time.Minute,
		almostDoneThreshold: 30 * time.Second,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start begins the background supervisor loop. Non-blocking.
func (s *Supervisor) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		s.log.Warn("timer supervisor already running")
		return
	}

	childCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true

	go s.loop(childCtx)

	// Start watcher if configured.
	if s.watcherRecipes != nil {
		s.watcher = NewWatcher(s.store, s.watcherRecipes, s.notifier, s.log, s.watcherOpts...)
		go s.watcher.Run(childCtx)
	}

	s.log.Info("timer supervisor started (tick=%s, cooldown=%s)", s.tickInterval, s.notifyCooldown)
}

// Stop gracefully shuts down the supervisor.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.cancel()
	s.running = false
	s.log.Info("timer supervisor stopped")
}

// loop is the main tick loop.
func (s *Supervisor) loop(ctx context.Context) {
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick runs one cycle: decrement timers, fire notifications.
func (s *Supervisor) tick(ctx context.Context) {
	sessions, err := s.store.ListActive(ctx)
	if err != nil {
		s.log.Error("supervisor: listing active sessions: %v", err)
		return
	}

	for _, session := range sessions {
		s.processSession(ctx, session)
	}
}

// processSession handles timer updates for a single session.
func (s *Supervisor) processSession(ctx context.Context, session *domain.Session) {
	if session.Status != domain.SessionActive {
		return
	}

	changed := false
	now := time.Now()

	for _, ts := range session.TimerStates {
		if ts.Status != domain.TimerRunning {
			continue
		}

		// Decrement remaining time.
		ts.Remaining -= s.tickInterval
		changed = true

		if ts.Remaining <= 0 {
			ts.Remaining = 0
			ts.Status = domain.TimerFired
			s.log.Debug("timer %s fired for session %s", ts.ID, session.ID)

			msg := s.escalationMessage(ts)
			if err := s.notifier.NotifyUrgent(ctx, msg); err != nil {
				s.log.Error("supervisor: notifying timer fire: %v", err)
			}
			ts.LastNotified = now
			ts.EscalationLevel = 1
			continue
		}

		// "Almost done" warning — once, when remaining crosses the threshold.
		if !ts.WarnedAlmost && ts.Remaining <= s.almostDoneThreshold && ts.Duration > s.almostDoneThreshold*2 {
			ts.WarnedAlmost = true
			changed = true
			msg := fmt.Sprintf("[Timer] %s — almost done, %s left.", ts.Label, formatRemaining(ts.Remaining))
			if err := s.notifier.Notify(ctx, msg); err != nil {
				s.log.Error("supervisor: almost-done notify: %v", err)
			}
			ts.LastRemindedAt = now
			continue
		}

		// Periodic reminder every reminderInterval.
		if s.reminderInterval > 0 && ts.Duration > s.reminderInterval {
			sinceLastReminder := now.Sub(ts.LastRemindedAt)
			if ts.LastRemindedAt.IsZero() {
				// First reminder after reminderInterval from start.
				elapsed := ts.Duration - ts.Remaining
				if elapsed >= s.reminderInterval {
					ts.LastRemindedAt = now
					changed = true
					msg := fmt.Sprintf("[Timer] %s — %s remaining.", ts.Label, formatRemaining(ts.Remaining))
					if err := s.notifier.Notify(ctx, msg); err != nil {
						s.log.Error("supervisor: reminder notify: %v", err)
					}
				}
			} else if sinceLastReminder >= s.reminderInterval {
				ts.LastRemindedAt = now
				changed = true
				msg := fmt.Sprintf("[Timer] %s — %s remaining.", ts.Label, formatRemaining(ts.Remaining))
				if err := s.notifier.Notify(ctx, msg); err != nil {
					s.log.Error("supervisor: reminder notify: %v", err)
				}
			}
		}
	}

	// Handle fired timers that need follow-up.
	for _, ts := range session.TimerStates {
		if ts.Status != domain.TimerFired {
			continue
		}

		if ts.EscalationLevel > s.maxEscalation {
			continue // Stop nagging.
		}

		if !ts.LastNotified.IsZero() && now.Sub(ts.LastNotified) < s.notifyCooldown {
			continue // Cooldown active.
		}

		msg := s.escalationMessage(ts)
		if err := s.notifier.Notify(ctx, msg); err != nil {
			s.log.Error("supervisor: escalation notify: %v", err)
		}
		ts.LastNotified = now
		ts.EscalationLevel++
		changed = true
	}

	if changed {
		if err := s.store.Save(ctx, session); err != nil {
			s.log.Error("supervisor: saving session %s: %v", session.ID, err)
		}
	}
}

// escalationMessage returns a message based on the escalation level.
func (s *Supervisor) escalationMessage(ts *domain.TimerState) string {
	switch ts.EscalationLevel {
	case 0:
		return fmt.Sprintf("[Timer] %s is up.", ts.Label)
	case 1:
		return fmt.Sprintf("[Timer] %s -- check it now.", ts.Label)
	case 2:
		return fmt.Sprintf("[Timer] %s. Now.", ts.Label)
	default:
		return fmt.Sprintf("[Timer] %s.", ts.Label)
	}
}

// formatRemaining returns a human-friendly spoken duration for timer reminders.
// Rounds to the nearest minute once there's at least 1 minute left.
func formatRemaining(d time.Duration) string {
	d = d.Round(time.Second)
	totalSec := int(d.Seconds())
	if totalSec < 60 {
		if totalSec == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", totalSec)
	}
	// Round to nearest minute.
	m := (totalSec + 30) / 60
	if m <= 0 {
		m = 1
	}
	if m == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}
