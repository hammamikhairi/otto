package domain

import "time"

// Session represents an active cooking session.
type Session struct {
	ID               string
	RecipeID         string
	RecipeName       string
	Servings         int
	CurrentStepIndex int
	StepStates       map[int]*StepState
	TimerStates      map[string]*TimerState
	Status           SessionStatus
	StartedAt        time.Time
	UpdatedAt        time.Time
}

// SessionStatus tracks the lifecycle of a cooking session.
type SessionStatus int

const (
	SessionActive SessionStatus = iota
	SessionPaused
	SessionCompleted
	SessionAbandoned
)

// String returns a human-readable session status.
func (s SessionStatus) String() string {
	switch s {
	case SessionActive:
		return "active"
	case SessionPaused:
		return "paused"
	case SessionCompleted:
		return "completed"
	case SessionAbandoned:
		return "abandoned"
	default:
		return "unknown"
	}
}

// StepState tracks progress of a single step within a session.
type StepState struct {
	Status      StepStatus
	StartedAt   time.Time
	CompletedAt time.Time
}

// StepStatus tracks the state of a single step.
type StepStatus int

const (
	StepPending StepStatus = iota
	StepActive
	StepDone
	StepSkipped
)

// String returns a human-readable step status.
func (s StepStatus) String() string {
	switch s {
	case StepPending:
		return "pending"
	case StepActive:
		return "active"
	case StepDone:
		return "done"
	case StepSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// TimerState tracks a running timer within a session.
type TimerState struct {
	ID              string
	StepID          string
	Label           string
	Duration        time.Duration
	Remaining       time.Duration
	Status          TimerStatus
	LastNotified    time.Time
	LastRemindedAt  time.Time // last periodic reminder
	WarnedAlmost    bool      // true after the "almost done" warning
	EscalationLevel int
}

// TimerStatus represents the state of a timer.
type TimerStatus int

const (
	TimerPending TimerStatus = iota
	TimerRunning
	TimerPaused
	TimerFired
	TimerDismissed
)

// String returns a human-readable timer status.
func (t TimerStatus) String() string {
	switch t {
	case TimerPending:
		return "pending"
	case TimerRunning:
		return "running"
	case TimerPaused:
		return "paused"
	case TimerFired:
		return "fired"
	case TimerDismissed:
		return "dismissed"
	default:
		return "unknown"
	}
}
