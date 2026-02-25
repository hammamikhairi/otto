// OttoCook — a conversational chef assistant.
//
// Usage:
//
//	ottocook [-verbose] [-quiet]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/hammamikhairi/ottocook/internal/conversation"
	"github.com/hammamikhairi/ottocook/internal/display"
	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/engine"
	"github.com/hammamikhairi/ottocook/internal/gpt"
	"github.com/hammamikhairi/ottocook/internal/logger"
	"github.com/hammamikhairi/ottocook/internal/recipe"
	"github.com/hammamikhairi/ottocook/internal/speech"
	"github.com/hammamikhairi/ottocook/internal/storage"
	"github.com/hammamikhairi/ottocook/internal/timer"
)

func main() {
	_ = godotenv.Load()

	verbose := flag.Bool("verbose", false, "enable verbose/debug logging")
	quiet := flag.Bool("quiet", false, "disable all logging")
	logFile := flag.String("log-file", ".otto-logs/otto.log", "file to write logs to (use \"stderr\" to log to console)")
	noSpeech := flag.Bool("no-speech", false, "disable text-to-speech even if Azure keys are set")
	diskCache := flag.Bool("disk-cache", true, "persist TTS audio cache to disk (reads from disk even when false)")
	cacheDir := flag.String("cache-dir", ".otto-cache", "directory for persistent TTS audio cache")
	noAI := flag.Bool("no-ai", false, "disable the AI agent even if GPT keys are set")
	voice := flag.Bool("voice", false, "enable voice input via local Whisper STT")
	whisperBin := flag.String("whisper-bin", "whisper-cli", "path to the whisper-cpp CLI binary")
	whisperModel := flag.String("whisper-model", "bin/ggml-small.bin", "path to the Whisper GGML model file")
	recordSecs := flag.Int("record-secs", 2, "seconds per voice recording chunk")
	flag.Parse()

	// Configure logger.
	logLevel := logger.LevelNormal
	if *verbose {
		logLevel = logger.LevelVerbose
	}
	if *quiet {
		logLevel = logger.LevelOff
	}

	// Direct logs to a file by default so the REPL stays clean.
	var logOut io.Writer = os.Stderr
	if *logFile != "" && *logFile != "stderr" {
		dir := filepath.Dir(*logFile)
		if dir != "" && dir != "." {
			os.MkdirAll(dir, 0o755)
		}
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not open log file %s: %v (falling back to stderr)\n", *logFile, err)
		} else {
			logOut = f
			defer f.Close()
		}
	}

	// Redirect Go's default log package (used by third-party libs like
	// the whisper transcriber) to the same output so it doesn't spam
	// the terminal.
	stdlog.SetOutput(logOut)
	stdlog.SetFlags(stdlog.Ltime)

	log := logger.New(logLevel, logOut)

	// Set up context — cancelled when the UI quits.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wire dependencies.
	recipes := recipe.NewMemorySource(log)
	store := storage.NewMemoryStore(log)
	ui := display.NewUI(store)
	textNotifier := conversation.NewCLINotifier(log, ui.Printf)
	parser := conversation.NewKeywordParser(log)
	eng := engine.New(recipes, store, log)

	// Build the active notifier. If TTS is available, wrap the text notifier
	// with a SpeakingNotifier that also speaks through the Mouth.
	var activeNotifier domain.Notifier = textNotifier
	var mouth *speech.Mouth

	azureKey := os.Getenv(speech.EnvAzureSpeechKey)
	azureRegion := os.Getenv(speech.EnvAzureSpeechRegion)

	if azureKey != "" && azureRegion != "" && !*noSpeech {
		ttsClient := speech.NewAzureClient(azureKey, azureRegion, log)

		player, err := speech.NewPlayer(log)
		if err != nil {
			log.Error("audio player init failed, speech disabled: %v", err)
		} else {
			mouth = speech.NewMouth(ttsClient, player, log,
				speech.WithCacheDir(*cacheDir),
				speech.WithDiskWrite(*diskCache),
			)
			mouth.Start(ctx)
			mouth.Prefetch(ctx, speech.ThinkingFillers()...)
			mouth.Prefetch(ctx, speech.ListeningFillers()...)
			activeNotifier = speech.NewSpeakingNotifier(textNotifier, mouth, log)
			log.Info("TTS enabled (voice=%s, region=%s)", speech.DefaultVoice, azureRegion)
		}
	} else if !*noSpeech {
		log.Info("TTS disabled: set %s and %s env vars to enable", speech.EnvAzureSpeechKey, speech.EnvAzureSpeechRegion)
	}

	supervisor := timer.New(store, activeNotifier, log,
		timer.WithWatcher(recipes),
	)

	// Build AI agent if GPT credentials are available.
	var agent *gpt.Agent

	gptKey := os.Getenv("GPT_CHAT_KEY")
	gptEndpoint := os.Getenv("GPT_CHAT_ENDPOINT")

	if gptKey != "" && gptEndpoint != "" && !*noAI {
		gptClient := gpt.NewClient(gptEndpoint, gptKey, log)
		agent = gpt.NewAgent(gptClient, log)
		log.Info("AI agent enabled")
	} else if !*noAI {
		log.Info("AI agent disabled: set GPT_CHAT_KEY and GPT_CHAT_ENDPOINT env vars to enable")
	}

	// Build voice input (STT) if enabled.
	var ear *speech.Ear
	if *voice {
		if _, err := os.Stat(*whisperModel); err != nil {
			fmt.Fprintf(os.Stderr, "error: whisper model not found at %s\n", *whisperModel)
			os.Exit(1)
		}
		os.MkdirAll(".otto-stt", 0o755)
		ear = speech.NewEar(*whisperBin, *whisperModel, mouth, log,
			speech.WithRecordDuration(time.Duration(*recordSecs)*time.Second),
		)
		go ear.Run(ctx)
		log.Info("voice input enabled (bin=%s, model=%s, chunk=%ds)", *whisperBin, *whisperModel, *recordSecs)
	}

	// Start background timer supervisor.
	supervisor.Start(ctx)
	defer supervisor.Stop()

	// Build the CLI app.
	app := &cliApp{
		engine:   eng,
		parser:   parser,
		notifier: activeNotifier,
		mouth:    mouth,
		agent:    agent,
		ear:      ear,
		log:      log,
		ui:       ui,
	}

	fmt.Println(display.RenderBanner())

	if ear != nil {
		fmt.Println(display.BannerStyle.Render("  Voice mode ON — say \"Hey Chef\" to activate, or type commands."))
		fmt.Println(display.BannerStyle.Render("  Type 'quit' to exit."))
	} else {
		fmt.Println(display.BannerStyle.Render("  Type 'help' for commands, 'quit' to exit."))
	}
	fmt.Println()

	// Run app logic in a background goroutine.
	go func() {
		ui.WaitReady()
		app.run(ctx)
		ui.Quit()
	}()

	// Bubble Tea owns the terminal — blocks until quit.
	if err := ui.Run(); err != nil {
		log.Error("display: %v", err)
	}
	cancel()
}

type cliApp struct {
	engine         *engine.Engine
	parser         domain.IntentParser
	notifier       domain.Notifier
	mouth          *speech.Mouth // nil when TTS is disabled
	agent          *gpt.Agent    // nil when AI is disabled
	ear            *speech.Ear   // nil when voice input is disabled
	log            *logger.Logger
	ui             *display.UI
	sessionID      string // current active session
	selectedRecipe string // recipe chosen before typing 'start'
}

// say prints a message to stdout and queues it for speech at the given priority.
// Use for conversational lines the user should hear. For raw formatting (menus,
// ingredient lists, tables) use fmt directly — those shouldn't be spoken.
func (a *cliApp) say(text string, priority speech.Priority) {
	a.ui.PrintChat(text)
	if a.mouth != nil {
		a.mouth.Say(text, priority)
	}
}

// sayUrgent prints a message in bold red and queues it at high priority.
func (a *cliApp) sayUrgent(text string) {
	a.ui.PrintUrgent(text)
	if a.mouth != nil {
		a.mouth.Say(text, speech.PriorityHigh)
	}
}

// prefetchStep pre-warms the TTS cache for the step at the given 0-based
// index within the current recipe. Non-blocking. Does nothing if TTS is
// disabled or the index is out of range.
func (a *cliApp) prefetchStep(ctx context.Context, recipeID string, stepIdx int) {
	if a.mouth == nil || recipeID == "" {
		return
	}
	r, err := a.engine.GetRecipe(ctx, recipeID)
	if err != nil || stepIdx < 0 || stepIdx >= len(r.Steps) {
		return
	}
	step := r.Steps[stepIdx]
	total := len(r.Steps)

	var conditions []string
	for _, c := range step.Conditions {
		conditions = append(conditions, c.Description)
	}
	tLabel := ""
	var tDur time.Duration
	if step.TimerConfig != nil {
		tLabel = step.TimerConfig.Label
		tDur = step.TimerConfig.Duration
	}
	text := speech.LineStep(step.Order, total, step.Instruction, conditions, step.ParallelHints, tLabel, tDur)
	a.mouth.Prefetch(ctx, text)
}

func (a *cliApp) run(ctx context.Context) {
	a.say(speech.LineWelcome(), speech.PriorityNormal)
	a.ui.Println("")
	a.showRecipes(ctx)

	// Voice channel (nil-safe: receiving on a nil channel blocks forever,
	// which is fine — select will only use the keyboard case).
	var voiceCh <-chan string
	if a.ear != nil {
		voiceCh = a.ear.C()
	}

	uiCh := a.ui.InputChan()

	for {
		var input string
		var ok bool

		select {
		case <-ctx.Done():
			return
		case input, ok = <-uiCh:
			if !ok {
				return
			}
		case input = <-voiceCh:
			// Print what was heard so the user sees it in the REPL.
			a.ui.PrintVoice(input)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		var session *domain.Session
		if a.sessionID != "" {
			s, err := a.engine.Status(ctx, a.sessionID)
			if err == nil {
				session = s
			}
		}

		intent, err := a.parser.Parse(ctx, input, session)
		if err != nil {
			a.log.Error("parsing input: %v", err)
			continue
		}

		a.log.Debug("intent: %s (payload=%q)", intent.Type, intent.Payload)
		a.handleIntent(ctx, intent)
	}
}

func (a *cliApp) handleIntent(ctx context.Context, intent *domain.Intent) {
	// Action intents interrupt whatever is currently being spoken so the
	// assistant doesn't keep talking over the new response.
	switch intent.Type {
	case domain.IntentListRecipes, domain.IntentSelectRecipe,
		domain.IntentStartCooking, domain.IntentAdvance, domain.IntentSkip,
		domain.IntentRepeat, domain.IntentRepeatLast, domain.IntentPause, domain.IntentResume,
		domain.IntentStatus, domain.IntentQuit, domain.IntentDismissTimer,
		domain.IntentAskQuestion, domain.IntentModify:
		if a.mouth != nil {
			a.mouth.Interrupt()
		}
	}

	switch intent.Type {
	case domain.IntentHelp:
		a.showHelp()
	case domain.IntentListRecipes:
		a.showRecipes(ctx)
	case domain.IntentSelectRecipe:
		a.selectRecipe(ctx, intent.Payload)
	case domain.IntentStartCooking:
		a.startCooking(ctx)
	case domain.IntentAdvance:
		a.advance(ctx)
	case domain.IntentSkip:
		a.skip(ctx)
	case domain.IntentRepeat:
		a.repeat(ctx)
	case domain.IntentRepeatLast:
		a.repeatLast(ctx)
	case domain.IntentPause:
		a.pause(ctx)
	case domain.IntentResume:
		a.resume(ctx)
	case domain.IntentStatus:
		a.status(ctx)
	case domain.IntentQuit:
		a.quit(ctx)
	case domain.IntentDismissTimer:
		a.dismissTimer(ctx, intent.Payload)
	case domain.IntentStartTimer:
		a.startTimer(ctx)
	case domain.IntentAskQuestion:
		a.askQuestion(ctx, intent.Payload)
	case domain.IntentModify:
		a.modifyRequest(ctx, intent.Payload)
	case domain.IntentUnknown:
		a.classifyAndDispatch(ctx, intent)
	}
}

// classifyAndDispatch sends unrecognised input to the AI for intent
// classification, then re-dispatches the result. Falls back to the
// generic "didn't catch that" line when the agent is unavailable or
// still returns unknown.
func (a *cliApp) classifyAndDispatch(ctx context.Context, original *domain.Intent) {
	if a.agent == nil {
		a.say(speech.LineUnknown(original.Payload), speech.PriorityLow)
		return
	}

	filler := speech.LineThinkingClassify()
	a.ui.PrintHint(filler)
	if a.mouth != nil {
		a.mouth.Say(filler, speech.PriorityCritical)
	}

	recipe, session := a.gatherContext(ctx)
	classified, err := a.agent.Classify(ctx, original.Payload, recipe, session)
	if err != nil {
		a.log.Error("AI classify failed: %v", err)
		a.say(speech.LineUnknown(original.Payload), speech.PriorityLow)
		return
	}

	if classified.Type == domain.IntentUnknown {
		a.say(speech.LineUnknown(original.Payload), speech.PriorityLow)
		return
	}

	a.log.Info("classified %q -> %s", original.Payload, classified.Type)
	a.handleIntent(ctx, classified)
}

// ── AI agent handlers ────────────────────────────────────────────

func (a *cliApp) askQuestion(ctx context.Context, question string) {
	if a.agent == nil {
		a.say(speech.LineAIDisabled(), speech.PriorityLow)
		return
	}

	filler := speech.LineThinkingQuestion()
	a.ui.PrintHint(filler)
	if a.mouth != nil {
		a.mouth.Say(filler, speech.PriorityCritical)
	}

	recipe, session := a.gatherContext(ctx)

	answer, err := a.agent.AskQuestion(ctx, question, recipe, session)
	if err != nil {
		a.log.Error("AI question failed: %v", err)
		a.say(speech.LineAIError(), speech.PriorityNormal)
		return
	}

	a.say(answer, speech.PriorityHigh)
}

// TODO(urgent): modification in the ingredients can affect the steps to cook the dish
func (a *cliApp) modifyRequest(ctx context.Context, request string) {
	if a.agent == nil {
		a.say(speech.LineAIDisabled(), speech.PriorityLow)
		return
	}

	filler := speech.LineThinkingModify()
	a.ui.PrintHint(filler)
	if a.mouth != nil {
		a.mouth.Say(filler, speech.PriorityCritical)
	}

	recipe, session := a.gatherContext(ctx)

	if recipe == nil {
		a.say(speech.LinePickRecipeFirst(), speech.PriorityNormal)
		return
	}

	resp, err := a.agent.Modify(ctx, request, recipe, session)
	if err != nil {
		a.log.Error("AI modify failed: %v", err)
		a.say(speech.LineAIError(), speech.PriorityNormal)
		return
	}

	// If the AI returned actions, apply them to the recipe.
	if len(resp.Actions) > 0 {
		if err := gpt.ApplyActions(recipe, resp.Actions); err != nil {
			a.log.Error("applying modifications failed: %v", err)
			a.ui.PrintUrgent(fmt.Sprintf("Error applying changes: %v", err))
			a.say(speech.LineAIError(), speech.PriorityNormal)
			return
		}

		// Persist the mutated recipe.
		if err := a.engine.UpdateRecipe(ctx, recipe); err != nil {
			a.log.Error("persisting recipe update failed: %v", err)
		}

		// Print a summary of what changed.
		a.ui.PrintStep(fmt.Sprintf("%d modification(s) applied", len(resp.Actions)))
		for i, act := range resp.Actions {
			line := fmt.Sprintf("%d. %s", i+1, act.Type)
			if act.IngredientName != "" {
				line += ": " + act.IngredientName
			}
			if act.StepIndex > 0 {
				line += fmt.Sprintf(" (step %d)", act.StepIndex)
			}
			a.ui.PrintInstruction(line)
		}

	}

	// Speak the summary.
	a.say(resp.Summary, speech.PriorityHigh)
}

// gatherContext loads the current recipe and session for AI context.
func (a *cliApp) gatherContext(ctx context.Context) (*domain.Recipe, *domain.Session) {
	var recipe *domain.Recipe
	var session *domain.Session

	recipeID := a.selectedRecipe
	if a.sessionID != "" {
		if s, err := a.engine.Status(ctx, a.sessionID); err == nil {
			session = s
			recipeID = s.RecipeID
		}
	}
	if recipeID != "" {
		if r, err := a.engine.GetRecipe(ctx, recipeID); err == nil {
			recipe = r
		}
	}
	return recipe, session
}

func (a *cliApp) showRecipes(ctx context.Context) {
	recipes, err := a.engine.ListRecipes(ctx)
	if err != nil {
		a.ui.PrintUrgent(fmt.Sprintf("Error loading recipes: %v", err))
		return
	}

	a.ui.PrintStep("Available recipes:")
	a.ui.Println("")
	for i, r := range recipes {
		a.ui.PrintInstruction(fmt.Sprintf("[%d] %s", i+1, r.Name))
		a.ui.PrintHint(r.Description)
		if len(r.Tags) > 0 {
			a.ui.PrintHint("Tags: " + strings.Join(r.Tags, ", "))
		}
		a.ui.Println("")
	}
	a.ui.PrintChat("Pick a recipe by number, or type 'help' for commands.")
}

func (a *cliApp) selectRecipe(ctx context.Context, payload string) {
	recipes, err := a.engine.ListRecipes(ctx)
	if err != nil {
		a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
		return
	}

	// Try numeric selection.
	var idx int
	if _, err := fmt.Sscanf(payload, "%d", &idx); err == nil {
		idx-- // 1-indexed to 0-indexed
		if idx >= 0 && idx < len(recipes) {
			a.selectedRecipe = recipes[idx].ID
			r, err := a.engine.GetRecipe(ctx, a.selectedRecipe)
			if err != nil {
				a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
				return
			}
			a.showRecipeDetail(r)

			// Build ingredient list for speech.
			ingNames := make([]string, len(r.Ingredients))
			for i, ing := range r.Ingredients {
				if ing.Quantity > 0 {
					if ing.SizeDescriptor != "" {
						ingNames[i] = fmt.Sprintf("%.0f %s %s", ing.Quantity, ing.SizeDescriptor, ing.Name)
					} else {
						ingNames[i] = fmt.Sprintf("%.0f %s %s", ing.Quantity, ing.Unit, ing.Name)
					}
				} else {
					ingNames[i] = ing.Name
				}
			}
			a.say(speech.LineRecipeSelected(r.Name, ingNames), speech.PriorityNormal)

			// Prefetch audio for the likely next action: starting to cook.
			if a.mouth != nil {
				a.mouth.Prefetch(ctx, speech.LineCookingStart(r.Name))
				a.prefetchStep(ctx, r.ID, 0) // step 1
			}
			return
		}
	}

	a.say(speech.LineInvalidSelection(payload), speech.PriorityLow)
}

func (a *cliApp) showRecipeDetail(r *domain.Recipe) {
	a.ui.PrintStep(fmt.Sprintf("=== %s ===", r.Name))
	a.ui.PrintInstruction(r.Description)
	a.ui.PrintHint(fmt.Sprintf("Servings: %d", r.Servings))

	a.ui.Println("")
	a.ui.PrintStep("Ingredients:")
	for _, ing := range r.Ingredients {
		opt := ""
		if ing.Optional {
			opt = " (optional)"
		}
		var line string
		if ing.Quantity > 0 {
			if ing.SizeDescriptor != "" {
				line = fmt.Sprintf("  - %.0f %s %s%s", ing.Quantity, ing.SizeDescriptor, ing.Name, opt)
			} else {
				line = fmt.Sprintf("  - %.0f %s %s%s", ing.Quantity, ing.Unit, ing.Name, opt)
			}
		} else {
			line = fmt.Sprintf("  - %s %s%s", ing.SizeDescriptor, ing.Name, opt)
		}
		a.ui.PrintInstruction(line)
	}
	a.ui.PrintHint(fmt.Sprintf("Steps: %d", len(r.Steps)))
}

func (a *cliApp) startCooking(ctx context.Context) {
	if a.selectedRecipe == "" {
		a.say(speech.LinePickRecipeFirst(), speech.PriorityNormal)
		return
	}

	if a.sessionID != "" {
		a.say(speech.LineAlreadyActive(), speech.PriorityNormal)
		return
	}

	session, err := a.engine.StartSession(ctx, a.selectedRecipe, 0)
	if err != nil {
		a.ui.PrintUrgent(fmt.Sprintf("Error starting session: %v", err))
		return
	}

	a.sessionID = session.ID
	a.say(speech.LineCookingStart(session.RecipeName), speech.PriorityNormal)
	a.showCurrentStep(ctx)

	// Prefetch step 2 while the user works on step 1.
	a.prefetchStep(ctx, a.selectedRecipe, 1)
}

func (a *cliApp) showCurrentStep(ctx context.Context) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	step, state, err := a.engine.CurrentStep(ctx, a.sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrNoMoreSteps) {
			a.say(speech.LineSessionDone(), speech.PriorityNormal)
			a.sessionID = ""
			a.selectedRecipe = ""
			return
		}
		a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
		return
	}

	session, _ := a.engine.Status(ctx, a.sessionID)
	total := len(session.StepStates)

	// Print visual step header.
	header := fmt.Sprintf("Step %d/%d", step.Order, total)
	if step.Duration > 0 {
		header += fmt.Sprintf(" (~%s)", formatDuration(step.Duration))
	}
	a.ui.PrintStep(header)
	a.ui.PrintInstruction(step.Instruction)

	if len(step.Conditions) > 0 {
		for _, c := range step.Conditions {
			a.ui.PrintHint("→ " + c.Description)
		}
	}

	if len(step.ParallelHints) > 0 {
		for _, hint := range step.ParallelHints {
			a.ui.PrintHint("tip: " + hint)
		}
	}

	if step.TimerConfig != nil {
		// Check whether timer is pending (not yet started by user).
		pending, _ := a.engine.HasPendingTimers(ctx, a.sessionID)
		if pending {
			a.ui.PrintHint(fmt.Sprintf("Timer ready: %s / %s \u2014 type 'timer' when you're ready to start", step.TimerConfig.Label, formatDuration(step.TimerConfig.Duration)))
		} else {
			a.ui.PrintHint(fmt.Sprintf("Timer: %s / %s", step.TimerConfig.Label, formatDuration(step.TimerConfig.Duration)))
		}
	}

	// Speak the step.
	if a.mouth != nil {
		var conditions []string
		for _, c := range step.Conditions {
			conditions = append(conditions, c.Description)
		}
		tLabel := ""
		var tDur time.Duration
		if step.TimerConfig != nil {
			tLabel = step.TimerConfig.Label
			tDur = step.TimerConfig.Duration
		}
		a.mouth.Say(speech.LineStep(step.Order, total, step.Instruction, conditions, step.ParallelHints, tLabel, tDur), speech.PriorityNormal)

		// Prefetch the next step while this one plays.
		a.prefetchStep(ctx, session.RecipeID, session.CurrentStepIndex+1)
	}

	// ── Next-step preview + parallel guidance ────────────────────
	nextStep, _ := a.engine.NextStep(ctx, a.sessionID)
	if nextStep != nil {
		a.ui.PrintHint("▸ Next: " + truncateStr(nextStep.Instruction, 80))

		// If current step has a timer, tell the user whether they can
		// move on or need to wait.
		if step.TimerConfig != nil {
			if nextStep.TimerConfig == nil || nextStep.ID != step.ID {
				guidance := speech.LineCanContinue(step.TimerConfig.Label)
				a.ui.PrintChat(guidance)
				if a.mouth != nil {
					a.mouth.Say(guidance, speech.PriorityLow)
				}
			}
		}
	}

	_ = state // available for future display of step timing stats
}

func (a *cliApp) advance(ctx context.Context) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	_, err := a.engine.Advance(ctx, a.sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrNoMoreSteps) {
			a.say(speech.LineLastStepDone(), speech.PriorityNormal)
			a.sessionID = ""
			a.selectedRecipe = ""
			return
		}
		if errors.Is(err, domain.ErrSessionNotActive) {
			a.say(speech.LineIsPaused(), speech.PriorityNormal)
			return
		}
		a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
		return
	}

	a.showCurrentStep(ctx)
}

func (a *cliApp) skip(ctx context.Context) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	_, err := a.engine.Skip(ctx, a.sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrNoMoreSteps) {
			a.say(speech.LineSkippedLastStep(), speech.PriorityNormal)
			a.sessionID = ""
			a.selectedRecipe = ""
			return
		}
		a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
		return
	}

	a.say(speech.LineSkipped(), speech.PriorityLow)
	a.showCurrentStep(ctx)
}

func (a *cliApp) repeat(ctx context.Context) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	a.showCurrentStep(ctx)
}

func (a *cliApp) repeatLast(ctx context.Context) {
	if a.mouth == nil {
		a.say(speech.LineNothingToRepeat(), speech.PriorityLow)
		return
	}

	last := a.mouth.LastSpoken()
	if last == "" {
		a.say(speech.LineNothingToRepeat(), speech.PriorityLow)
		return
	}

	a.say(last, speech.PriorityNormal)
}

func (a *cliApp) startTimer(ctx context.Context) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	n, err := a.engine.StartPendingTimers(ctx, a.sessionID)
	if err != nil {
		a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
		return
	}

	if n == 0 {
		a.ui.PrintHint("No pending timers to start.")
		return
	}

	a.say(fmt.Sprintf("Timer started! (%d)", n), speech.PriorityNormal)
}

func (a *cliApp) dismissTimer(ctx context.Context, payload string) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	active, err := a.engine.ActiveTimers(ctx, a.sessionID)
	if err != nil || len(active) == 0 {
		a.say(speech.LineNoActiveTimers(), speech.PriorityLow)
		return
	}

	// If there's only one active timer, just dismiss it.
	if len(active) == 1 {
		if err := a.engine.DismissTimer(ctx, a.sessionID, active[0].ID); err != nil {
			a.log.Error("dismiss timer: %v", err)
			a.say(speech.LineTimerAck(), speech.PriorityNormal)
			return
		}
		a.say(speech.LineTimerDismissed(active[0].Label), speech.PriorityNormal)
		return
	}

	// Multiple timers — prioritise fired ones first.
	// A plain "ok"/"dismiss" should dismiss whatever has fired,
	// since that's obviously what the user is reacting to.
	var fired []*domain.TimerState
	for _, t := range active {
		if t.Status == domain.TimerFired {
			fired = append(fired, t)
		}
	}
	if len(fired) > 0 {
		for _, t := range fired {
			if err := a.engine.DismissTimer(ctx, a.sessionID, t.ID); err != nil {
				a.log.Error("dismiss timer %s: %v", t.ID, err)
			}
		}
		if len(fired) == 1 {
			a.say(speech.LineTimerDismissed(fired[0].Label), speech.PriorityNormal)
		} else {
			a.say(speech.LineTimerAck(), speech.PriorityNormal)
		}
		return
	}

	// No fired timers — multiple running. Ask AI which one(s) to dismiss.
	if a.agent == nil {
		// No AI: dismiss all.
		for _, t := range active {
			_ = a.engine.DismissTimer(ctx, a.sessionID, t.ID)
		}
		a.say(speech.LineTimerAck(), speech.PriorityNormal)
		return
	}

	recipe, session := a.gatherContext(ctx)
	resp, err := a.agent.DismissTimer(ctx, payload, recipe, session)
	if err != nil {
		a.log.Error("AI dismiss timer failed: %v", err)
		a.say(speech.LineTimerAck(), speech.PriorityNormal)
		return
	}

	if len(resp.TimerIDs) == 0 {
		// AI couldn't figure it out — speak its clarification question.
		a.say(resp.Summary, speech.PriorityNormal)
		return
	}

	for _, tid := range resp.TimerIDs {
		if err := a.engine.DismissTimer(ctx, a.sessionID, tid); err != nil {
			a.log.Error("dismiss timer %s: %v", tid, err)
		}
	}
	a.say(resp.Summary, speech.PriorityNormal)
}

func (a *cliApp) pause(ctx context.Context) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	if err := a.engine.Pause(ctx, a.sessionID); err != nil {
		a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
		return
	}

	a.say(speech.LinePaused(), speech.PriorityNormal)
}

func (a *cliApp) resume(ctx context.Context) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	_, err := a.engine.Resume(ctx, a.sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrSessionPaused) {
			a.say(speech.LineNotPaused(), speech.PriorityLow)
			return
		}
		a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
		return
	}

	a.say(speech.LineResumed(), speech.PriorityNormal)
	a.showCurrentStep(ctx)
}

func (a *cliApp) status(ctx context.Context) {
	if a.sessionID == "" {
		a.say(speech.LineNoSession(), speech.PriorityLow)
		return
	}

	session, err := a.engine.Status(ctx, a.sessionID)
	if err != nil {
		a.ui.PrintUrgent(fmt.Sprintf("Error: %v", err))
		return
	}

	// Visual status dump (not spoken — too much data).
	a.ui.PrintStep(fmt.Sprintf("Session: %s", session.ID[:8]))
	a.ui.PrintInstruction(fmt.Sprintf("Recipe:  %s", session.RecipeName))
	a.ui.PrintInstruction(fmt.Sprintf("Status:  %s", session.Status))
	a.ui.PrintInstruction(fmt.Sprintf("Step:    %d/%d", session.CurrentStepIndex+1, len(session.StepStates)))
	a.ui.PrintHint(fmt.Sprintf("Started: %s ago", formatDuration(time.Since(session.StartedAt))))

	activeTimers := 0
	for _, ts := range session.TimerStates {
		if ts.Status == domain.TimerRunning {
			a.ui.PrintChat(fmt.Sprintf("%s — %s remaining", ts.Label, formatDuration(ts.Remaining)))
			activeTimers++
		} else if ts.Status == domain.TimerFired {
			a.ui.PrintUrgent(fmt.Sprintf("%s — DONE", ts.Label))
			activeTimers++
		}
	}
	if activeTimers == 0 {
		a.ui.PrintHint("Timers:  none active")
	}

	// Speak a concise summary.
	if a.mouth != nil {
		a.mouth.Say(speech.LineStatus(
			session.CurrentStepIndex+1, len(session.StepStates),
			session.RecipeName, activeTimers,
		), speech.PriorityLow)
	}
}

func (a *cliApp) quit(ctx context.Context) {
	if a.sessionID != "" {
		if err := a.engine.Abandon(ctx, a.sessionID); err != nil {
			a.log.Error("abandoning session: %v", err)
		}
		a.say(speech.LineAbandoned(), speech.PriorityNormal)
		a.sessionID = ""
		a.selectedRecipe = ""
	}
	a.say(speech.LineBye(), speech.PriorityNormal)
	// Brief pause so TTS can start the goodbye line.
	time.Sleep(300 * time.Millisecond)
	a.ui.Quit()
}

func (a *cliApp) showHelp() {
	a.ui.PrintStep("Commands:")
	a.ui.PrintInstruction("  list / recipes   Show available recipes")
	a.ui.PrintInstruction("  1, 2, 3...       Select a recipe by number")
	a.ui.PrintInstruction("  start / go       Start cooking the selected recipe")
	a.ui.PrintInstruction("  next / done      Move to the next step")
	a.ui.PrintInstruction("  skip             Skip the current step")
	a.ui.PrintInstruction("  repeat / again   Show the current step again")
	a.ui.PrintInstruction("  repeat last      Replay the last thing the assistant said")
	a.ui.PrintInstruction("  pause / brb      Pause the session and timers")
	a.ui.PrintInstruction("  resume / back    Resume a paused session")
	a.ui.PrintInstruction("  status / where   Show session progress and timers")
	a.ui.PrintInstruction("  timer / ready    Start a pending step timer")
	a.ui.PrintInstruction("  dismiss / ok     Acknowledge a timer notification")
	a.ui.PrintInstruction("  dismiss ...      Dismiss a specific timer (e.g. \"dismiss the simmer timer\")")
	a.ui.PrintInstruction("  help             Show this message")
	a.ui.PrintInstruction("  quit / exit      Abandon session and exit")
	a.ui.Println("")
	a.ui.PrintStep("AI (requires GPT_CHAT_KEY + GPT_CHAT_ENDPOINT):")
	a.ui.PrintInstruction("  how do I...?     Ask the AI a cooking question")
	a.ui.PrintInstruction("  modify ...       Ask the AI to change the recipe")
	a.ui.PrintInstruction("  change ...       (swap, replace, double, halve, adjust, substitute)")
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

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
