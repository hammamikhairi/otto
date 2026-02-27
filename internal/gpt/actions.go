package gpt

import "time"

// ActionType identifies what kind of recipe modification the AI wants to make.
type ActionType string

const (
	ActionUpdateIngredient ActionType = "update_ingredient"
	ActionRemoveIngredient ActionType = "remove_ingredient"
	ActionAddIngredient    ActionType = "add_ingredient"
	ActionUpdateStep       ActionType = "update_step"
	ActionRemoveStep       ActionType = "remove_step"
	ActionAddStep          ActionType = "add_step"
	ActionUpdateServings   ActionType = "update_servings"
	ActionUpdateTimer      ActionType = "update_timer"
)

// ModifyResponse is the structured JSON the AI returns for modification
// requests. It contains one or more actions to apply and a human-readable
// spoken summary.
type ModifyResponse struct {
	// Actions is the ordered list of mutations to apply to the recipe.
	Actions []Action `json:"actions"`
	// Summary is a short, TTS-friendly confirmation spoken to the user.
	Summary string `json:"summary"`
}

// Action is a single recipe mutation. The fields used depend on the Type.
type Action struct {
	Type ActionType `json:"type"`

	// Ingredient fields (update/add/remove)
	IngredientName    string  `json:"ingredient_name,omitempty"`
	NewIngredientName string  `json:"new_ingredient_name,omitempty"`
	Quantity          float64 `json:"quantity,omitempty"`
	Unit              string  `json:"unit,omitempty"`
	SizeDescriptor    string  `json:"size_descriptor,omitempty"`

	// Step fields (update/add/remove)
	StepIndex   int    `json:"step_index,omitempty"` // 1-based
	Instruction string `json:"instruction,omitempty"`

	// Timer fields
	TimerLabel    string `json:"timer_label,omitempty"`
	TimerDuration string `json:"timer_duration,omitempty"` // e.g. "5m", "30s"

	// Servings
	Servings int `json:"servings,omitempty"`
}

// ParsedTimerDuration returns the timer duration as time.Duration, or 0.
func (a Action) ParsedTimerDuration() time.Duration {
	d, _ := time.ParseDuration(a.TimerDuration)
	return d
}
