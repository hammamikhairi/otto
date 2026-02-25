package domain

// IntentType classifies what the user wants to do.
type IntentType int

const (
	IntentUnknown IntentType = iota
	IntentListRecipes
	IntentSelectRecipe
	IntentStartCooking
	IntentAdvance
	IntentSkip
	IntentRepeat
	IntentPause
	IntentResume
	IntentStatus
	IntentQuit
	IntentHelp
	IntentDismissTimer
	IntentRepeatLast  // replay the last thing the mouth said
	IntentAskQuestion // free-form question sent to the AI agent
	IntentModify      // user wants the AI to change something (recipe, servings, etc.)
	IntentStartTimer  // user confirms they're ready — start pending timers
)

// String returns a human-readable intent type.
func (i IntentType) String() string {
	switch i {
	case IntentListRecipes:
		return "list_recipes"
	case IntentSelectRecipe:
		return "select_recipe"
	case IntentStartCooking:
		return "start_cooking"
	case IntentAdvance:
		return "advance"
	case IntentSkip:
		return "skip"
	case IntentRepeat:
		return "repeat"
	case IntentPause:
		return "pause"
	case IntentResume:
		return "resume"
	case IntentStatus:
		return "status"
	case IntentQuit:
		return "quit"
	case IntentHelp:
		return "help"
	case IntentDismissTimer:
		return "dismiss_timer"
	case IntentRepeatLast:
		return "repeat_last"
	case IntentAskQuestion:
		return "ask_question"
	case IntentModify:
		return "modify"
	case IntentStartTimer:
		return "start_timer"
	default:
		return "unknown"
	}
}

// Intent represents a parsed user action.
type Intent struct {
	Type    IntentType
	Payload string // optional context, e.g. recipe ID for select
}

// intentNames maps snake_case names to IntentType values.
var intentNames = map[string]IntentType{
	"list_recipes":  IntentListRecipes,
	"select_recipe": IntentSelectRecipe,
	"start_cooking": IntentStartCooking,
	"advance":       IntentAdvance,
	"skip":          IntentSkip,
	"repeat":        IntentRepeat,
	"pause":         IntentPause,
	"resume":        IntentResume,
	"status":        IntentStatus,
	"quit":          IntentQuit,
	"help":          IntentHelp,
	"dismiss_timer": IntentDismissTimer,
	"repeat_last":   IntentRepeatLast,
	"ask_question":  IntentAskQuestion,
	"modify":        IntentModify,
	"start_timer":   IntentStartTimer,
	"unknown":       IntentUnknown,
}

// IntentFromString converts a snake_case intent name to an IntentType.
// Returns IntentUnknown for unrecognized names.
func IntentFromString(name string) IntentType {
	if t, ok := intentNames[name]; ok {
		return t
	}
	return IntentUnknown
}
