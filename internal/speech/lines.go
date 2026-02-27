// Package speech — lines.go centralises every spoken string.
// Edit this file to change OttoCook's personality. Keep lines short and
// direct; the TTS engine handles inflection.
package speech

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// ── Greeting / Global ────────────────────────────────────────────

func LineWelcome() string {
	return "Hello. What are we cooking today?"
}

func LineBye() string {
	return "Bye."
}

func LineShutdown() string {
	return "Shutting down."
}

func LineNothingToRepeat() string {
	return "I haven't said anything yet."
}

// ── Recipe selection ─────────────────────────────────────────────

// LineRecipeSelected is spoken after the user picks a recipe number.
// It reads out the ingredients so they can gather them.
func LineRecipeSelected(name string, ingredients []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s. You'll need: ", name)
	for i, ing := range ingredients {
		if i > 0 && i == len(ingredients)-1 {
			b.WriteString(", and ")
		} else if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(ing)
	}
	b.WriteString(". Say start when you're ready.")
	return b.String()
}

func LineInvalidSelection(payload string) string {
	return fmt.Sprintf("Invalid selection: %s. Pick a number from the list.", payload)
}

func LinePickRecipeFirst() string {
	return "Pick a recipe first."
}

func LineAlreadyActive() string {
	return "You already have an active session. Say quit to abandon it first."
}

// ── Cooking session ──────────────────────────────────────────────

func LineCookingStart(recipeName string) string {
	return fmt.Sprintf("Cooking %s. Here we go.", recipeName)
}

func LineNoSession() string {
	return "No active session."
}

func LineSessionDone() string {
	return "All done."
}

func LineLastStepDone() string {
	return "That was the last step. You're done."
}

func LineSkippedLastStep() string {
	return "Skipped the last step."
}

func LineSkipped() string {
	return "Skipped."
}

func LinePaused() string {
	return "Paused. Timers are on hold. Say resume when ready."
}

func LineNotPaused() string {
	return "Session isn't paused."
}

func LineIsPaused() string {
	return "Session is paused. Say resume first."
}

func LineResumed() string {
	return "Resumed."
}

func LineAbandoned() string {
	return "Session abandoned."
}

func LineTimerAck() string {
	return "Timer acknowledged."
}

func LineTimerDismissed(label string) string {
	return fmt.Sprintf("%s timer dismissed.", label)
}

func LineNoActiveTimers() string {
	return "No active timers to dismiss."
}

// LineNextPreview builds a short spoken preview of the upcoming step.
func LineNextPreview(nextOrder int, instruction string) string {
	// Truncate to ~80 chars for speech.
	if len(instruction) > 80 {
		instruction = instruction[:77] + "..."
	}
	return fmt.Sprintf("Coming up next, step %d: %s", nextOrder, instruction)
}

// LineCanContinue tells the user they can move on — the timer will auto-start.
func LineCanContinue(timerLabel string) string {
	return fmt.Sprintf("The %s timer will start automatically when you move on. Carry on.", timerLabel)
}

// LineMustWait tells the user they need to wait for the timer before moving on.
func LineMustWait(timerLabel string) string {
	return fmt.Sprintf("Wait for the %s timer before moving on — the next step needs it done.", timerLabel)
}

func LineUnknown(input string) string {
	return fmt.Sprintf("Didn't catch that: %s.", input)
}

// ── AI agent ─────────────────────────────────────────────────────

func LineAIDisabled() string {
	return "The AI assistant is not available. Set GPT_CHAT_KEY and GPT_CHAT_ENDPOINT to enable it."
}

func LineAIError() string {
	return "Something went wrong with the AI. Try again."
}

// ── Thinking fillers ─────────────────────────────────────────────
// Spoken while waiting for the AI to respond. Randomized to avoid repetition.

var thinkingQuestion = []string{
	"Let me think about that.",
	"Good question. Give me a second.",
	"Hmm, one moment.",
	"Let me look into that for you.",
	"Hang on, thinking.",
	"Bear with me a sec.",
	"Let me consider that.",
	"One second, looking that up.",
	"That's a fair question. Hold on.",
	"Let me work that out.",
	"Give me a beat.",
	"Okay, let me think.",
}

var thinkingModify = []string{
	"Let me see what I can do.",
	"Alright, working on that.",
	"Give me a moment to figure this out.",
	"Okay, let me adjust things.",
	"One second, reworking the recipe.",
	"Hang on, making changes.",
	"Let me sort that out for you.",
	"On it. Give me a second.",
	"Alright, let me tweak that.",
	"Hold on, recalculating.",
	"Let me see how that affects things.",
	"Working on it.",
}

var thinkingClassify = []string{
	"Hmm, one second.",
	"Let me figure out what you mean.",
	"Hold on.",
	"Give me a moment.",
	"One second.",
	"Let me think about that.",
}

// LineThinkingQuestion returns a random filler for when a question is being processed.
func LineThinkingQuestion() string {
	return thinkingQuestion[rand.Intn(len(thinkingQuestion))]
}

// LineThinkingModify returns a random filler for when a modification is being processed.
func LineThinkingModify() string {
	return thinkingModify[rand.Intn(len(thinkingModify))]
}

// LineThinkingClassify returns a random filler for when the AI is classifying unknown input.
func LineThinkingClassify() string {
	return thinkingClassify[rand.Intn(len(thinkingClassify))]
}

// ThinkingFillers returns every filler string (question + modify + classify) so they
// can be prefetched into the TTS cache at startup.
func ThinkingFillers() []string {
	out := make([]string, 0, len(thinkingQuestion)+len(thinkingModify)+len(thinkingClassify))
	out = append(out, thinkingQuestion...)
	out = append(out, thinkingModify...)
	out = append(out, thinkingClassify...)
	return out
}

// ── Step narration ───────────────────────────────────────────────

// LineStep builds the spoken text for a cooking step. It includes
// conditions, tips, and timer info so the user gets everything in
// one continuous utterance.
func LineStep(order, total int, instruction string, conditions []string, tips []string, timerLabel string, timerDur time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Step %d of %d. %s", order, total, instruction)
	for _, c := range conditions {
		fmt.Fprintf(&b, " %s.", c)
	}
	for _, t := range tips {
		fmt.Fprintf(&b, " Tip: %s.", t)
	}
	if timerLabel != "" {
		fmt.Fprintf(&b, " Timer set: %s, %s.", timerLabel, FormatDurationSpeech(timerDur))
	}
	return b.String()
}

// ── Status ───────────────────────────────────────────────────────

func LineStatus(step, total int, recipeName string, activeTimers int) string {
	s := fmt.Sprintf("Step %d of %d, cooking %s.", step, total, recipeName)
	if activeTimers == 1 {
		s += " 1 timer running."
	} else if activeTimers > 1 {
		s += fmt.Sprintf(" %d timers running.", activeTimers)
	}
	return s
}

// ── Helpers ──────────────────────────────────────────────────────

// ── Listening acknowledgment ─────────────────────────────────────
// Spoken when the wake word is detected, so the user knows they've
// been heard and should start talking.

var listeningFillers = []string{
	"I'm listening.",
	"Listening.",
	"Yes chef?",
	"What do you need?",
	"I'm here.",
	"What's up?",
	"Yes?",
}

// LineListening returns a random acknowledgment for when the wake
// word is detected.
func LineListening() string {
	return listeningFillers[rand.Intn(len(listeningFillers))]
}

// ListeningFillers returns all listening acknowledgment strings so
// they can be prefetched into the TTS cache at startup.
func ListeningFillers() []string {
	out := make([]string, len(listeningFillers))
	copy(out, listeningFillers)
	return out
}

// FormatDurationSpeech returns a human-friendly spoken duration.
func FormatDurationSpeech(d time.Duration) string {
	d = d.Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	switch {
	case m == 0:
		return fmt.Sprintf("%d seconds", s)
	case s == 0 && m == 1:
		return "1 minute"
	case s == 0:
		return fmt.Sprintf("%d minutes", m)
	default:
		return fmt.Sprintf("%d minutes %d seconds", m, s)
	}
}
