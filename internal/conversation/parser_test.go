package conversation

import (
	"context"
	"testing"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

func TestKeywordParser(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	parser := NewKeywordParser(log)
	ctx := context.Background()

	tests := []struct {
		input       string
		wantType    domain.IntentType
		wantPayload string
	}{
		// Advance variants
		{"next", domain.IntentAdvance, ""},
		{"done", domain.IntentAdvance, ""},
		{"continue", domain.IntentAdvance, ""},
		{"n", domain.IntentAdvance, ""},

		// Skip
		{"skip", domain.IntentSkip, ""},
		{"s", domain.IntentSkip, ""},

		// Repeat
		{"repeat", domain.IntentRepeat, ""},
		{"again", domain.IntentRepeat, ""},
		{"what?", domain.IntentRepeat, ""},

		// Pause/Resume
		{"pause", domain.IntentPause, ""},
		{"brb", domain.IntentPause, ""},
		{"resume", domain.IntentResume, ""},
		{"back", domain.IntentResume, ""},

		// Status
		{"status", domain.IntentStatus, ""},
		{"where", domain.IntentStatus, ""},

		// Quit
		{"quit", domain.IntentQuit, ""},
		{"exit", domain.IntentQuit, ""},
		{"q", domain.IntentQuit, ""},

		// Help
		{"help", domain.IntentHelp, ""},
		{"?", domain.IntentHelp, ""},

		// Dismiss
		{"ok", domain.IntentDismissTimer, ""},
		{"dismiss", domain.IntentDismissTimer, ""},

		// List
		{"list", domain.IntentListRecipes, ""},
		{"recipes", domain.IntentListRecipes, ""},

		// Select by number
		{"1", domain.IntentSelectRecipe, "1"},
		{"2", domain.IntentSelectRecipe, "2"},
		{"99", domain.IntentSelectRecipe, "99"},

		// Select by name
		{"select 2", domain.IntentSelectRecipe, "2"},
		{"pick pasta", domain.IntentSelectRecipe, "pasta"},

		// Start
		{"start", domain.IntentStartCooking, ""},
		{"go", domain.IntentStartCooking, ""},

		// Unknown
		{"flambé the cat", domain.IntentUnknown, "flambé the cat"},
		{"", domain.IntentUnknown, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			intent, err := parser.Parse(ctx, tt.input, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if intent.Type != tt.wantType {
				t.Errorf("input=%q: got type %s, want %s", tt.input, intent.Type, tt.wantType)
			}
			if tt.wantPayload != "" && intent.Payload != tt.wantPayload {
				t.Errorf("input=%q: got payload %q, want %q", tt.input, intent.Payload, tt.wantPayload)
			}
		})
	}
}
