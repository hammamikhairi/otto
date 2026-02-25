// Package engine implements the core cooking session state machine.
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Option configures the engine.
type Option func(*Engine)

// WithServingsDefault sets the default number of servings for new sessions.
func WithServingsDefault(n int) Option {
	return func(e *Engine) {
		e.defaultServings = n
	}
}

// Engine manages cooking sessions. It depends only on interfaces and is
// fully testable with mocks.
type Engine struct {
	recipes         domain.RecipeSource
	store           domain.SessionStore
	log             *logger.Logger
	defaultServings int
}

// RecipeUpdater is an optional interface that RecipeSource implementations
// can satisfy to support in-place recipe mutations.
type RecipeUpdater interface {
	Update(ctx context.Context, recipe *domain.Recipe) error
}

// New creates a cooking engine with the given dependencies and options.
func New(recipes domain.RecipeSource, store domain.SessionStore, log *logger.Logger, opts ...Option) *Engine {
	e := &Engine{
		recipes:         recipes,
		store:           store,
		log:             log,
		defaultServings: 2,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ListRecipes returns all available recipes.
func (e *Engine) ListRecipes(ctx context.Context) ([]domain.RecipeSummary, error) {
	return e.recipes.List(ctx)
}

// GetRecipe returns a full recipe by ID.
func (e *Engine) GetRecipe(ctx context.Context, id string) (*domain.Recipe, error) {
	return e.recipes.Get(ctx, id)
}

// UpdateRecipe persists a mutated recipe. Returns an error if the
// underlying RecipeSource does not support updates.
func (e *Engine) UpdateRecipe(ctx context.Context, recipe *domain.Recipe) error {
	updater, ok := e.recipes.(RecipeUpdater)
	if !ok {
		return fmt.Errorf("recipe source does not support updates")
	}
	return updater.Update(ctx, recipe)
}

// StartSession begins a new cooking session for the given recipe.
func (e *Engine) StartSession(ctx context.Context, recipeID string, servings int) (*domain.Session, error) {
	recipe, err := e.recipes.Get(ctx, recipeID)
	if err != nil {
		return nil, fmt.Errorf("getting recipe: %w", err)
	}

	if servings <= 0 {
		servings = e.defaultServings
	}

	session := &domain.Session{
		ID:               generateID(),
		RecipeID:         recipe.ID,
		RecipeName:       recipe.Name,
		Servings:         servings,
		CurrentStepIndex: 0,
		StepStates:       make(map[int]*domain.StepState),
		TimerStates:      make(map[string]*domain.TimerState),
		Status:           domain.SessionActive,
		StartedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	// Initialize step states.
	for i := range recipe.Steps {
		session.StepStates[i] = &domain.StepState{Status: domain.StepPending}
	}

	// Mark first step as active.
	session.StepStates[0].Status = domain.StepActive
	session.StepStates[0].StartedAt = time.Now()

	// Start timer for the first step if configured.
	e.maybeStartTimer(session, recipe.Steps[0])

	if err := e.store.Save(ctx, session); err != nil {
		return nil, fmt.Errorf("saving session: %w", err)
	}

	e.log.Info("started session %s for recipe %q (%d servings)", session.ID, recipe.Name, servings)
	return session, nil
}

// CurrentStep returns the current step and its state.
func (e *Engine) CurrentStep(ctx context.Context, sessionID string) (*domain.Step, *domain.StepState, error) {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("loading session: %w", err)
	}

	recipe, err := e.recipes.Get(ctx, session.RecipeID)
	if err != nil {
		return nil, nil, fmt.Errorf("getting recipe: %w", err)
	}

	idx := session.CurrentStepIndex
	if idx >= len(recipe.Steps) {
		return nil, nil, domain.ErrNoMoreSteps
	}

	step := &recipe.Steps[idx]
	state := session.StepStates[idx]
	return step, state, nil
}

// Advance moves the session to the next step.
func (e *Engine) Advance(ctx context.Context, sessionID string) (*domain.Step, error) {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading session: %w", err)
	}

	if session.Status != domain.SessionActive {
		return nil, domain.ErrSessionNotActive
	}

	recipe, err := e.recipes.Get(ctx, session.RecipeID)
	if err != nil {
		return nil, fmt.Errorf("getting recipe: %w", err)
	}

	// Complete current step.
	now := time.Now()
	current := session.StepStates[session.CurrentStepIndex]
	current.Status = domain.StepDone
	current.CompletedAt = now

	// NOTE: timers for the completed step keep running. They are
	// only dismissed by the user ("dismiss" command) or when they
	// fire and the user acknowledges them.

	// Move to next step.
	nextIdx := session.CurrentStepIndex + 1
	if nextIdx >= len(recipe.Steps) {
		session.Status = domain.SessionCompleted
		session.UpdatedAt = now
		if err := e.store.Save(ctx, session); err != nil {
			return nil, fmt.Errorf("saving session: %w", err)
		}
		e.log.Info("session %s completed", sessionID)
		return nil, domain.ErrNoMoreSteps
	}

	session.CurrentStepIndex = nextIdx
	session.StepStates[nextIdx].Status = domain.StepActive
	session.StepStates[nextIdx].StartedAt = now
	session.UpdatedAt = now

	step := &recipe.Steps[nextIdx]
	e.maybeStartTimer(session, *step)

	if err := e.store.Save(ctx, session); err != nil {
		return nil, fmt.Errorf("saving session: %w", err)
	}

	e.log.Debug("session %s advanced to step %d/%d", sessionID, nextIdx+1, len(recipe.Steps))
	return step, nil
}

// Skip skips the current step and moves to the next one.
func (e *Engine) Skip(ctx context.Context, sessionID string) (*domain.Step, error) {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading session: %w", err)
	}

	if session.Status != domain.SessionActive {
		return nil, domain.ErrSessionNotActive
	}

	recipe, err := e.recipes.Get(ctx, session.RecipeID)
	if err != nil {
		return nil, fmt.Errorf("getting recipe: %w", err)
	}

	// Mark current as skipped.
	now := time.Now()
	session.StepStates[session.CurrentStepIndex].Status = domain.StepSkipped
	session.StepStates[session.CurrentStepIndex].CompletedAt = now

	// NOTE: timers for the skipped step keep running. They are
	// only dismissed by the user ("dismiss" command) or when they
	// fire and the user acknowledges them.

	nextIdx := session.CurrentStepIndex + 1
	if nextIdx >= len(recipe.Steps) {
		session.Status = domain.SessionCompleted
		session.UpdatedAt = now
		if err := e.store.Save(ctx, session); err != nil {
			return nil, fmt.Errorf("saving session: %w", err)
		}
		e.log.Info("session %s completed (last step skipped)", sessionID)
		return nil, domain.ErrNoMoreSteps
	}

	session.CurrentStepIndex = nextIdx
	session.StepStates[nextIdx].Status = domain.StepActive
	session.StepStates[nextIdx].StartedAt = now
	session.UpdatedAt = now

	step := &recipe.Steps[nextIdx]
	e.maybeStartTimer(session, *step)

	if err := e.store.Save(ctx, session); err != nil {
		return nil, fmt.Errorf("saving session: %w", err)
	}

	e.log.Debug("session %s skipped to step %d/%d", sessionID, nextIdx+1, len(recipe.Steps))
	return step, nil
}

// Repeat returns the current step again without changing state.
func (e *Engine) Repeat(ctx context.Context, sessionID string) (*domain.Step, error) {
	step, _, err := e.CurrentStep(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	e.log.Debug("session %s repeating current step", sessionID)
	return step, nil
}

// Pause pauses the session and all running timers.
func (e *Engine) Pause(ctx context.Context, sessionID string) error {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("loading session: %w", err)
	}

	if session.Status != domain.SessionActive {
		return domain.ErrSessionNotActive
	}

	session.Status = domain.SessionPaused
	session.UpdatedAt = time.Now()

	// Pause all running timers (pending timers stay pending).
	for _, ts := range session.TimerStates {
		if ts.Status == domain.TimerRunning {
			ts.Status = domain.TimerPaused
		}
	}

	if err := e.store.Save(ctx, session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	e.log.Info("session %s paused", sessionID)
	return nil
}

// Resume resumes a paused session.
func (e *Engine) Resume(ctx context.Context, sessionID string) (*domain.Session, error) {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading session: %w", err)
	}

	if session.Status != domain.SessionPaused {
		return nil, domain.ErrSessionPaused
	}

	session.Status = domain.SessionActive
	session.UpdatedAt = time.Now()

	// Resume paused timers.
	for _, ts := range session.TimerStates {
		if ts.Status == domain.TimerPaused {
			ts.Status = domain.TimerRunning
		}
	}

	if err := e.store.Save(ctx, session); err != nil {
		return nil, fmt.Errorf("saving session: %w", err)
	}

	e.log.Info("session %s resumed", sessionID)
	return session, nil
}

// Status returns the full session state.
func (e *Engine) Status(ctx context.Context, sessionID string) (*domain.Session, error) {
	return e.store.Load(ctx, sessionID)
}

// Abandon marks a session as abandoned.
func (e *Engine) Abandon(ctx context.Context, sessionID string) error {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("loading session: %w", err)
	}

	session.Status = domain.SessionAbandoned
	session.UpdatedAt = time.Now()

	if err := e.store.Save(ctx, session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	e.log.Info("session %s abandoned", sessionID)
	return nil
}

// maybeStartTimer creates a pending timer for a step if it has a timer config.
// The timer does NOT start counting down until the user explicitly confirms.
func (e *Engine) maybeStartTimer(session *domain.Session, step domain.Step) {
	if step.TimerConfig == nil {
		return
	}

	timerID := fmt.Sprintf("timer-%s", step.ID)
	session.TimerStates[timerID] = &domain.TimerState{
		ID:        timerID,
		StepID:    step.ID,
		Label:     step.TimerConfig.Label,
		Duration:  step.TimerConfig.Duration,
		Remaining: step.TimerConfig.Duration,
		Status:    domain.TimerPending,
	}

	e.log.Debug("created pending timer %s (%s) for step %s", timerID, step.TimerConfig.Duration, step.ID)
}

// StartPendingTimers transitions all pending timers for the current step
// from TimerPending to TimerRunning. Returns the number of timers started.
func (e *Engine) StartPendingTimers(ctx context.Context, sessionID string) (int, error) {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("loading session: %w", err)
	}

	started := 0
	for _, ts := range session.TimerStates {
		if ts.Status == domain.TimerPending {
			ts.Status = domain.TimerRunning
			started++
			e.log.Debug("started timer %s (%s)", ts.ID, ts.Duration)
		}
	}

	if started > 0 {
		session.UpdatedAt = time.Now()
		if err := e.store.Save(ctx, session); err != nil {
			return 0, fmt.Errorf("saving session: %w", err)
		}
	}

	return started, nil
}

// HasPendingTimers returns true if the session has any timers waiting to start.
func (e *Engine) HasPendingTimers(ctx context.Context, sessionID string) (bool, error) {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("loading session: %w", err)
	}
	for _, ts := range session.TimerStates {
		if ts.Status == domain.TimerPending {
			return true, nil
		}
	}
	return false, nil
}

// dismissStepTimers dismisses all timers associated with a step.
func (e *Engine) dismissStepTimers(session *domain.Session, stepID string) {
	for _, ts := range session.TimerStates {
		if ts.StepID == stepID && (ts.Status == domain.TimerRunning || ts.Status == domain.TimerFired) {
			ts.Status = domain.TimerDismissed
			e.log.Debug("dismissed timer %s for step %s", ts.ID, stepID)
		}
	}
}

// DismissTimer dismisses a single timer by ID.
func (e *Engine) DismissTimer(ctx context.Context, sessionID, timerID string) error {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("loading session: %w", err)
	}

	ts, ok := session.TimerStates[timerID]
	if !ok {
		return fmt.Errorf("timer %q not found", timerID)
	}

	if ts.Status != domain.TimerRunning && ts.Status != domain.TimerFired {
		return fmt.Errorf("timer %q is %s, cannot dismiss", timerID, ts.Status)
	}

	ts.Status = domain.TimerDismissed
	session.UpdatedAt = time.Now()

	if err := e.store.Save(ctx, session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	e.log.Info("dismissed timer %s (%s)", timerID, ts.Label)
	return nil
}

// ActiveTimers returns all running or fired timers for a session.
func (e *Engine) ActiveTimers(ctx context.Context, sessionID string) ([]*domain.TimerState, error) {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading session: %w", err)
	}

	var active []*domain.TimerState
	for _, ts := range session.TimerStates {
		if ts.Status == domain.TimerRunning || ts.Status == domain.TimerFired {
			active = append(active, ts)
		}
	}
	return active, nil
}

// NextStep returns the step after the current one, or nil if this is the last step.
func (e *Engine) NextStep(ctx context.Context, sessionID string) (*domain.Step, error) {
	session, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading session: %w", err)
	}

	recipe, err := e.recipes.Get(ctx, session.RecipeID)
	if err != nil {
		return nil, fmt.Errorf("getting recipe: %w", err)
	}

	nextIdx := session.CurrentStepIndex + 1
	if nextIdx >= len(recipe.Steps) {
		return nil, nil // last step
	}

	step := recipe.Steps[nextIdx]
	return &step, nil
}
