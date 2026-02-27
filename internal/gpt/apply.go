package gpt

import (
	"fmt"
	"strings"

	"github.com/hammamikhairi/ottocook/internal/domain"
)

// ApplyActions mutates the recipe in-place according to the actions in the
// ModifyResponse. Returns an error on the first action that can't be applied.
// Callers should persist the recipe after a successful call.
func ApplyActions(recipe *domain.Recipe, actions []Action) error {
	for i, act := range actions {
		if err := applyOne(recipe, act); err != nil {
			return fmt.Errorf("action %d (%s): %w", i+1, act.Type, err)
		}
	}
	return nil
}

func applyOne(r *domain.Recipe, act Action) error {
	switch act.Type {
	case ActionUpdateIngredient:
		return updateIngredient(r, act)
	case ActionRemoveIngredient:
		return removeIngredient(r, act)
	case ActionAddIngredient:
		return addIngredient(r, act)
	case ActionUpdateStep:
		return updateStep(r, act)
	case ActionRemoveStep:
		return removeStep(r, act)
	case ActionAddStep:
		return addStep(r, act)
	case ActionUpdateServings:
		return updateServings(r, act)
	case ActionUpdateTimer:
		return updateTimer(r, act)
	default:
		return fmt.Errorf("unknown action type: %s", act.Type)
	}
}

// ── Ingredient actions ───────────────────────────────────────────

func findIngredient(r *domain.Recipe, name string) int {
	lower := strings.ToLower(name)
	for i, ing := range r.Ingredients {
		if strings.ToLower(ing.Name) == lower ||
			strings.Contains(strings.ToLower(ing.Name), lower) {
			return i
		}
	}
	return -1
}

func updateIngredient(r *domain.Recipe, act Action) error {
	idx := findIngredient(r, act.IngredientName)
	if idx == -1 {
		return fmt.Errorf("ingredient %q not found", act.IngredientName)
	}
	ing := &r.Ingredients[idx]
	oldName := ing.Name
	if act.NewIngredientName != "" {
		ing.Name = act.NewIngredientName
		// Safety net: replace the old ingredient name in all step
		// instructions so the recipe stays consistent even if the
		// AI forgot to emit update_step actions.
		replaceInSteps(r, oldName, act.NewIngredientName)
	}
	if act.Quantity > 0 {
		ing.Quantity = act.Quantity
	}
	if act.Unit != "" {
		ing.Unit = act.Unit
	}
	if act.SizeDescriptor != "" {
		ing.SizeDescriptor = act.SizeDescriptor
	}
	return nil
}

// replaceInSteps does a case-insensitive replacement of oldName with
// newName in every step instruction.
func replaceInSteps(r *domain.Recipe, oldName, newName string) {
	lower := strings.ToLower(oldName)
	for i, step := range r.Steps {
		instrLower := strings.ToLower(step.Instruction)
		if strings.Contains(instrLower, lower) {
			// Preserve original casing of surrounding text by doing
			// a positional replacement.
			result := make([]byte, 0, len(step.Instruction))
			src := step.Instruction
			for {
				pos := strings.Index(strings.ToLower(src), lower)
				if pos == -1 {
					result = append(result, src...)
					break
				}
				result = append(result, src[:pos]...)
				result = append(result, newName...)
				src = src[pos+len(oldName):]
			}
			r.Steps[i].Instruction = string(result)
		}
	}
}

func removeIngredient(r *domain.Recipe, act Action) error {
	idx := findIngredient(r, act.IngredientName)
	if idx == -1 {
		return fmt.Errorf("ingredient %q not found", act.IngredientName)
	}
	r.Ingredients = append(r.Ingredients[:idx], r.Ingredients[idx+1:]...)
	return nil
}

func addIngredient(r *domain.Recipe, act Action) error {
	r.Ingredients = append(r.Ingredients, domain.Ingredient{
		Name:           act.IngredientName,
		Quantity:       act.Quantity,
		Unit:           act.Unit,
		SizeDescriptor: act.SizeDescriptor,
	})
	return nil
}

// ── Step actions ─────────────────────────────────────────────────

func updateStep(r *domain.Recipe, act Action) error {
	idx := act.StepIndex - 1 // 1-based -> 0-based
	if idx < 0 || idx >= len(r.Steps) {
		return fmt.Errorf("step %d out of range (1-%d)", act.StepIndex, len(r.Steps))
	}
	if act.Instruction != "" {
		r.Steps[idx].Instruction = act.Instruction
	}
	return nil
}

func removeStep(r *domain.Recipe, act Action) error {
	idx := act.StepIndex - 1
	if idx < 0 || idx >= len(r.Steps) {
		return fmt.Errorf("step %d out of range (1-%d)", act.StepIndex, len(r.Steps))
	}
	r.Steps = append(r.Steps[:idx], r.Steps[idx+1:]...)
	// Renumber remaining steps.
	for i := range r.Steps {
		r.Steps[i].Order = i + 1
	}
	return nil
}

func addStep(r *domain.Recipe, act Action) error {
	idx := act.StepIndex - 1
	if idx < 0 || idx > len(r.Steps) {
		idx = len(r.Steps) // append at end
	}
	newStep := domain.Step{
		ID:          fmt.Sprintf("step-%d", len(r.Steps)+1),
		Order:       idx + 1,
		Instruction: act.Instruction,
	}
	// Insert at position.
	r.Steps = append(r.Steps, domain.Step{})
	copy(r.Steps[idx+1:], r.Steps[idx:])
	r.Steps[idx] = newStep
	// Renumber.
	for i := range r.Steps {
		r.Steps[i].Order = i + 1
	}
	return nil
}

// ── Servings ─────────────────────────────────────────────────────

func updateServings(r *domain.Recipe, act Action) error {
	if act.Servings <= 0 {
		return fmt.Errorf("invalid servings: %d", act.Servings)
	}
	if r.Servings > 0 {
		scale := float64(act.Servings) / float64(r.Servings)
		for i := range r.Ingredients {
			r.Ingredients[i].Quantity *= scale
		}
	}
	r.Servings = act.Servings
	return nil
}

// ── Timer ────────────────────────────────────────────────────────

func updateTimer(r *domain.Recipe, act Action) error {
	idx := act.StepIndex - 1
	if idx < 0 || idx >= len(r.Steps) {
		return fmt.Errorf("step %d out of range (1-%d)", act.StepIndex, len(r.Steps))
	}
	dur := act.ParsedTimerDuration()
	if dur <= 0 {
		return fmt.Errorf("invalid timer duration: %q", act.TimerDuration)
	}
	step := &r.Steps[idx]
	if step.TimerConfig == nil {
		step.TimerConfig = &domain.TimerConfig{}
	}
	step.TimerConfig.Duration = dur
	if act.TimerLabel != "" {
		step.TimerConfig.Label = act.TimerLabel
	}
	return nil
}
