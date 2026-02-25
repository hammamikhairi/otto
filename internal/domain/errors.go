package domain

import "errors"

// Sentinel errors used across layers.
var (
	ErrNotFound         = errors.New("not found")
	ErrSessionNotActive = errors.New("session is not active")
	ErrSessionPaused    = errors.New("session is paused")
	ErrNoMoreSteps      = errors.New("no more steps in recipe")
	ErrAlreadyExists    = errors.New("already exists")
	ErrNotImplemented   = errors.New("not implemented")
)
