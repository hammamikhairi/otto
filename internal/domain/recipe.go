// Package domain defines the core types and interfaces for the chef assistant.
// All other packages depend on domain; domain depends on nothing.
package domain

import "time"

// Recipe represents a complete cooking recipe.
type Recipe struct {
	ID          string
	Name        string
	Description string
	Servings    int
	Ingredients []Ingredient
	Steps       []Step
	Tags        []string
	Version     int
}

// RecipeSummary is a lightweight view of a recipe for listing.
type RecipeSummary struct {
	ID          string
	Name        string
	Description string
	Tags        []string
}

// Ingredient represents a single ingredient with human-style quantities.
type Ingredient struct {
	Name           string
	Quantity       float64
	Unit           string // "pieces", "cups", "tablespoons", "grams", ""
	SizeDescriptor string // "small", "medium", "large", "handful", ""
	Optional       bool
}

// Step represents a single cooking step.
type Step struct {
	ID            string
	Order         int
	Instruction   string
	Duration      time.Duration // expected duration, 0 if untimed
	Conditions    []StepCondition
	ParallelHints []string // suggestions like "while waiting, chop X"
	TimerConfig   *TimerConfig
}

// StepCondition defines when a step is considered done.
type StepCondition struct {
	Type        ConditionType
	Description string
}

// ConditionType enumerates how step completion is determined.
type ConditionType int

const (
	// ConditionManual requires the user to confirm completion.
	ConditionManual ConditionType = iota
	// ConditionTime means the step completes after a duration.
	ConditionTime
	// ConditionVisual describes a visual cue ("golden brown").
	ConditionVisual
	// ConditionTemperature describes a temperature target.
	ConditionTemperature
)

// TimerConfig defines an optional timer attached to a step.
type TimerConfig struct {
	Duration time.Duration
	Label    string
}
