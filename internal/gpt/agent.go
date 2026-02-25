package gpt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Agent wraps the OpenAI Client with cooking-domain context building.
// It is the single entry-point the CLI calls for AI-powered features.
type Agent struct {
	client *Client
	log    *logger.Logger
}

// NewAgent creates a cooking AI agent backed by the given Client.
func NewAgent(client *Client, log *logger.Logger) *Agent {
	return &Agent{client: client, log: log}
}

// ── Public API ───────────────────────────────────────────────────

// AskQuestion sends a free-form question to the model together with the
// full cooking context and returns the assistant's answer.
func (a *Agent) AskQuestion(ctx context.Context, question string, recipe *domain.Recipe, session *domain.Session) (string, error) {
	messages := a.buildMessages(PromptQuestion, question, recipe, session)
	return a.client.Chat(ctx, messages)
}

// Modify sends a modification request to the model and returns a structured
// ModifyResponse containing actions to apply and a spoken summary.
func (a *Agent) Modify(ctx context.Context, request string, recipe *domain.Recipe, session *domain.Session) (*ModifyResponse, error) {
	messages := a.buildMessages(PromptModify, request, recipe, session)
	raw, err := a.client.Chat(ctx, messages)
	if err != nil {
		return nil, err
	}

	// Strip markdown code fences if the model wraps the JSON (common).
	raw = stripCodeFence(raw)

	var resp ModifyResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		a.log.Error("gpt: failed to parse modify JSON: %v\nraw: %s", err, raw)
		// Fall back: treat the whole response as a spoken summary with no actions.
		return &ModifyResponse{Summary: raw}, nil
	}

	a.log.Debug("gpt: modify response: %d actions, summary=%q", len(resp.Actions), truncate(resp.Summary, 80))
	return &resp, nil
}

// DismissTimerResponse is the JSON the model returns for timer dismissal.
type DismissTimerResponse struct {
	TimerIDs []string `json:"timer_ids"`
	Summary  string   `json:"summary"`
}

// DismissTimer asks the model which timer(s) the user wants to dismiss.
func (a *Agent) DismissTimer(ctx context.Context, request string, recipe *domain.Recipe, session *domain.Session) (*DismissTimerResponse, error) {
	messages := a.buildMessages(PromptDismissTimer, request, recipe, session)
	raw, err := a.client.Chat(ctx, messages)
	if err != nil {
		return nil, err
	}

	raw = stripCodeFence(raw)

	var resp DismissTimerResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		a.log.Error("gpt: failed to parse dismiss timer JSON: %v\nraw: %s", err, raw)
		return &DismissTimerResponse{Summary: raw}, nil
	}

	a.log.Debug("gpt: dismiss timer response: ids=%v, summary=%q", resp.TimerIDs, resp.Summary)
	return &resp, nil
}

// classifyResponse is the JSON the model returns for intent classification.
type classifyResponse struct {
	Intent  string `json:"intent"`
	Payload string `json:"payload"`
}

// Classify sends unrecognised user input to the model for intent classification.
// Returns a classified Intent, or IntentUnknown if classification fails.
func (a *Agent) Classify(ctx context.Context, input string, recipe *domain.Recipe, session *domain.Session) (*domain.Intent, error) {
	messages := a.buildMessages(PromptClassify, input, recipe, session)
	raw, err := a.client.Chat(ctx, messages)
	if err != nil {
		return nil, err
	}

	raw = stripCodeFence(raw)

	var resp classifyResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		a.log.Error("gpt: failed to parse classify JSON: %v\nraw: %s", err, raw)
		return &domain.Intent{Type: domain.IntentUnknown, Payload: input}, nil
	}

	intentType := domain.IntentFromString(resp.Intent)
	a.log.Debug("gpt: classified %q -> %s (payload=%q)", input, intentType, resp.Payload)

	payload := resp.Payload
	if payload == "" {
		payload = input
	}

	return &domain.Intent{Type: intentType, Payload: payload}, nil
}

// stripCodeFence removes ```json ... ``` wrappers that LLMs love to add.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove opening fence line.
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence.
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}

// ── Context building ─────────────────────────────────────────────

// buildMessages assembles the system prompt, an optional cooking-context
// user message, and the actual user query.
func (a *Agent) buildMessages(systemPrompt, userQuery string, recipe *domain.Recipe, session *domain.Session) []Message {
	msgs := []Message{
		TextMessage(RoleSystem, systemPrompt),
	}

	// Inject cooking context if available.
	if ctxBlock := a.buildContext(recipe, session); ctxBlock != "" {
		msgs = append(msgs, TextMessage(RoleUser, ctxBlock))
		// Fake an ack so the model treats context as established.
		msgs = append(msgs, TextMessage(RoleAssistant, "Got it, I have the context."))
	}

	msgs = append(msgs, TextMessage(RoleUser, userQuery))
	return msgs
}

// buildContext serializes the current recipe and session state into a
// plain-text block the model can reason over. Includes full timer state,
// step progress, and current-step details so the model can give informed
// answers about what's happening right now.
func (a *Agent) buildContext(recipe *domain.Recipe, session *domain.Session) string {
	if recipe == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("[Current Recipe Context]\n")
	fmt.Fprintf(&b, "Recipe: %s\n", recipe.Name)
	fmt.Fprintf(&b, "Description: %s\n", recipe.Description)
	fmt.Fprintf(&b, "Servings: %d\n", recipe.Servings)

	// Ingredients
	b.WriteString("\nIngredients:\n")
	for _, ing := range recipe.Ingredients {
		opt := ""
		if ing.Optional {
			opt = " (optional)"
		}
		if ing.Quantity > 0 {
			if ing.SizeDescriptor != "" {
				fmt.Fprintf(&b, "- %.0f %s %s%s\n", ing.Quantity, ing.SizeDescriptor, ing.Name, opt)
			} else {
				fmt.Fprintf(&b, "- %.0f %s %s%s\n", ing.Quantity, ing.Unit, ing.Name, opt)
			}
		} else {
			fmt.Fprintf(&b, "- %s%s\n", ing.Name, opt)
		}
	}

	// Steps — show timer configs so the model knows which steps use timers.
	b.WriteString("\nSteps:\n")
	for _, step := range recipe.Steps {
		fmt.Fprintf(&b, "%d. %s", step.Order, step.Instruction)
		if step.TimerConfig != nil {
			fmt.Fprintf(&b, " [has timer: %s, %s]", step.TimerConfig.Label, formatDuration(step.TimerConfig.Duration))
		} else {
			b.WriteString(" [no timer]")
		}
		b.WriteString("\n")
		for _, c := range step.Conditions {
			fmt.Fprintf(&b, "   condition: %s\n", c.Description)
		}
	}

	// Session state — this is the critical part for contextual answers.
	if session != nil {
		b.WriteString("\n[Session State]\n")
		fmt.Fprintf(&b, "Status: %s\n", session.Status)

		totalSteps := len(recipe.Steps)
		currentIdx := session.CurrentStepIndex
		fmt.Fprintf(&b, "Current step: %d of %d\n", currentIdx+1, totalSteps)
		fmt.Fprintf(&b, "Elapsed: %s\n", formatDuration(time.Since(session.StartedAt)))

		// Current step detail.
		if currentIdx >= 0 && currentIdx < totalSteps {
			cur := recipe.Steps[currentIdx]
			fmt.Fprintf(&b, "\n[Current Step Detail]\n")
			fmt.Fprintf(&b, "Step %d: %s\n", cur.Order, cur.Instruction)
			if cur.TimerConfig != nil {
				fmt.Fprintf(&b, "This step has a timer: %s (%s)\n", cur.TimerConfig.Label, formatDuration(cur.TimerConfig.Duration))
			} else {
				b.WriteString("This step does NOT have a timer.\n")
			}
			for _, c := range cur.Conditions {
				fmt.Fprintf(&b, "Done when: %s\n", c.Description)
			}
		}

		// Step progress.
		b.WriteString("\n[Step Progress]\n")
		for i, step := range recipe.Steps {
			status := "pending"
			if ss, ok := session.StepStates[i]; ok {
				status = ss.Status.String()
			}
			fmt.Fprintf(&b, "Step %d (%s): %s\n", step.Order, status, truncate(step.Instruction, 50))
		}

		// Timer state — explicit about presence/absence.
		b.WriteString("\n[Timers]\n")
		var running, paused, fired []string
		for _, ts := range session.TimerStates {
			switch ts.Status {
			case domain.TimerRunning:
				running = append(running, fmt.Sprintf("%s: %s remaining", ts.Label, formatDuration(ts.Remaining)))
			case domain.TimerPaused:
				paused = append(paused, fmt.Sprintf("%s: paused (%s remaining)", ts.Label, formatDuration(ts.Remaining)))
			case domain.TimerFired:
				fired = append(fired, fmt.Sprintf("%s: DONE — waiting for acknowledgment", ts.Label))
			}
		}
		if len(running) == 0 && len(paused) == 0 && len(fired) == 0 {
			b.WriteString("No active timers.\n")
		} else {
			for _, s := range running {
				fmt.Fprintf(&b, "RUNNING: %s\n", s)
			}
			for _, s := range paused {
				fmt.Fprintf(&b, "PAUSED: %s\n", s)
			}
			for _, s := range fired {
				fmt.Fprintf(&b, "FIRED: %s\n", s)
			}
		}
	} else {
		b.WriteString("\n[No active cooking session — user is browsing recipes.]\n")
	}

	return b.String()
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}
