package speech

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	audiotranscriber "github.com/sklyt/whisper/pkg"

	"github.com/hammamikhairi/ottocook/internal/logger"
)

// earState represents the Ear's listening mode.
type earState int

const (
	// earDormant — passively scanning short clips for the wake word.
	earDormant earState = iota
	// earListening — wake word detected, actively capturing the command.
	earListening
)

// Default wake phrases. Any of these at the start of a transcription
// (case-insensitive) triggers active listening.
var defaultWakeWords = []string{
	"hey chef",
	"otto cook",
	"ottocook",
	"hey, chef",
	"hey shef",
	"la chef",
	"lashef",
	"Mr chef",
	"Otto",
}

// envAnnotation matches whisper environmental annotations like
// "(keyboard clicking)", "[laughter]", "(speaking French)", etc.
var envAnnotation = regexp.MustCompile(`[\(\[][a-zA-Z][a-zA-Z\s]*[\)\]]`)

// EarOption configures the Ear.
type EarOption func(*Ear)

// WithRecordDuration sets how long each active-listening chunk lasts.
func WithRecordDuration(d time.Duration) EarOption {
	return func(e *Ear) { e.recordDuration = d }
}

// WithSilenceGap sets the pause between recording chunks.
func WithSilenceGap(d time.Duration) EarOption {
	return func(e *Ear) { e.silenceGap = d }
}

// WithTempDir sets the directory for temporary WAV files.
func WithTempDir(dir string) EarOption {
	return func(e *Ear) { e.tempDir = dir }
}

// WithWakeWords overrides the default wake phrases.
func WithWakeWords(words ...string) EarOption {
	return func(e *Ear) { e.wakeWords = words }
}

// WithListenTimeout sets how long the ear stays in active listening
// mode before giving up and returning to dormant.
func WithListenTimeout(d time.Duration) EarOption {
	return func(e *Ear) { e.listenTimeout = d }
}

// WithDormantDuration sets how long each dormant probe recording lasts.
// Shorter = more responsive wake-word detection, but more CPU.
func WithDormantDuration(d time.Duration) EarOption {
	return func(e *Ear) { e.dormantDuration = d }
}

// Ear provides wake-word-triggered speech-to-text input using a local
// Whisper model.
//
// Lifecycle:
//  1. DORMANT — records short clips and checks for a wake word.
//     Everything else is silently discarded (no spam).
//  2. LISTENING — wake word detected → interrupt the Mouth (shut it up)
//     → record longer chunks and accumulate the user's command until
//     silence or timeout.
//  3. The accumulated text (minus the wake word) is sent through the
//     channel, and the ear goes back to dormant.
type Ear struct {
	whisperBin string
	modelPath  string
	tempDir    string
	log        *logger.Logger
	mouth      *Mouth // optional — interrupt on wake word

	wakeWords       []string
	recordDuration  time.Duration // active listening chunk length
	dormantDuration time.Duration // wake-word probe chunk length
	silenceGap      time.Duration
	listenTimeout   time.Duration // max active listening window

	mu     sync.Mutex
	muted  bool
	state  earState
	textCh chan string // transcribed text flows here
}

// NewEar creates a wake-word-triggered voice input listener.
//
//   - whisperBin: path to the whisper-cli executable
//   - modelPath:  path to the GGML model file
//   - mouth:      optional Mouth — will be interrupted when wake word is heard
func NewEar(whisperBin, modelPath string, mouth *Mouth, log *logger.Logger, opts ...EarOption) *Ear {
	e := &Ear{
		whisperBin:      whisperBin,
		modelPath:       modelPath,
		tempDir:         ".otto-stt",
		log:             log,
		mouth:           mouth,
		wakeWords:       defaultWakeWords,
		recordDuration:  1 * time.Second,
		dormantDuration: 3 * time.Second,
		silenceGap:      300 * time.Millisecond,
		listenTimeout:   15 * time.Second,
		state:           earDormant,
		textCh:          make(chan string, 8),
	}
	for _, opt := range opts {
		opt(e)
	}

	// Validate that the whisper binary is reachable.
	if _, err := exec.LookPath(e.whisperBin); err != nil {
		log.Error("ear: whisper binary %q not found in PATH: %v", e.whisperBin, err)
	}

	return e
}

// C returns the channel that receives transcribed text. Read from this
// in your main loop to get voice input.
func (e *Ear) C() <-chan string {
	return e.textCh
}

// Mute temporarily disables listening (e.g. during TTS playback).
func (e *Ear) Mute() {
	e.mu.Lock()
	e.muted = true
	e.mu.Unlock()
	e.log.Debug("ear: muted")
}

// Unmute re-enables listening.
func (e *Ear) Unmute() {
	e.mu.Lock()
	e.muted = false
	e.mu.Unlock()
	e.log.Debug("ear: unmuted")
}

func (e *Ear) isMuted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.muted
}

// Run starts the wake-word listening loop. Blocks until ctx is cancelled.
// Call this in a goroutine.
func (e *Ear) Run(ctx context.Context) {
	e.log.Info("ear: started (dormant=%s, active=%s, timeout=%s, wake=%v)",
		e.dormantDuration, e.recordDuration, e.listenTimeout, e.wakeWords)

	for {
		select {
		case <-ctx.Done():
			e.log.Info("ear: stopped")
			return
		default:
		}

		if e.isMuted() {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		switch e.getState() {
		case earDormant:
			e.doDormant(ctx)
		case earListening:
			e.doListening(ctx)
		}
	}
}

// ── State helpers ────────────────────────────────────────────────

func (e *Ear) getState() earState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *Ear) setState(s earState) {
	e.mu.Lock()
	e.state = s
	e.mu.Unlock()
}

// waitForMouth blocks until the mouth finishes speaking (e.g. the
// listening filler) so the microphone doesn't pick it up.
func (e *Ear) waitForMouth(ctx context.Context) {
	if e.mouth == nil {
		return
	}
	for e.mouth.IsSpeaking() || e.mouth.QueueLen() > 0 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
}

// ── Dormant mode ─────────────────────────────────────────────────

// doDormant records a short clip, transcribes it, and checks for the
// wake word. If found, transitions to earListening. Everything else
// is silently discarded.
func (e *Ear) doDormant(ctx context.Context) {
	// Echo prevention: don't record while the mouth is speaking.
	if e.mouth != nil && (e.mouth.IsSpeaking() || e.mouth.QueueLen() > 0) {
		time.Sleep(200 * time.Millisecond)
		return
	}

	text := e.recordChunk(ctx, e.dormantDuration)

	// Post-recording echo check: if the mouth started speaking during
	// our recording, the audio is contaminated — discard it.
	if e.mouth != nil && (e.mouth.IsSpeaking() || e.mouth.QueueLen() > 0) {
		e.log.Debug("ear/dormant: discarding — mouth started during recording")
		return
	}

	text = cleanTranscription(text)
	if text == "" {
		return
	}

	e.log.Debug("ear/dormant: heard %q", text)

	remaining := e.stripWakeWord(text)
	if remaining == "" {
		// No wake word found — discard.
		return
	}

	e.log.Info("ear: wake word detected in %q", text)

	// Interrupt the mouth so it shuts up immediately.
	if e.mouth != nil {
		e.mouth.Interrupt()
		e.log.Debug("ear: interrupted mouth")
	}

	// If the user said the wake word + a command in one breath
	// (e.g. "hey chef next step"), send it directly — but clean
	// hallucinations first so "Thank you." etc. don't slip through.
	remaining = strings.TrimSpace(remaining)
	remaining = cleanTranscription(remaining)
	if remaining != "" && !isJustWakeWord(remaining) {
		e.log.Info("ear: immediate command: %q", remaining)
		select {
		case e.textCh <- remaining:
		case <-ctx.Done():
		}
		return
	}

	// Otherwise, switch to active listening for the command.
	// Speak a filler so the user knows we're listening.
	if e.mouth != nil {
		filler := LineListening()
		e.mouth.Say(filler, PriorityCritical)
		e.log.Debug("ear: said %q", filler)
	}

	e.setState(earListening)
}

// ── Active listening mode ────────────────────────────────────────

// doListening records chunks until the user stops talking (empty
// transcription) or the listen timeout expires, then sends the
// accumulated text and returns to dormant.
func (e *Ear) doListening(ctx context.Context) {
	e.log.Info("ear: listening...")

	// Grace period: wait for the mouth to finish saying the filler
	// and give the user a moment to start speaking.
	e.waitForMouth(ctx)
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		e.setState(earDormant)
		return
	}

	deadline := time.After(e.listenTimeout)
	var parts []string
	emptyRuns := 0
	hasHeardSpeech := false
	// Before the user starts talking, allow more silence. Once they've
	// started, a shorter gap means they're done.
	const graceEmpty = 4      // empty chunks tolerated before first speech
	const postSpeechEmpty = 2 // empty chunks tolerated after speech started

	for {
		select {
		case <-ctx.Done():
			e.setState(earDormant)
			return
		case <-deadline:
			e.log.Debug("ear: listen timeout reached")
			goto done
		default:
		}

		chunk := e.recordChunk(ctx, e.recordDuration)
		chunk = cleanTranscription(chunk)

		if chunk == "" {
			emptyRuns++
			maxEmpty := graceEmpty
			if hasHeardSpeech {
				maxEmpty = postSpeechEmpty
			}
			if emptyRuns >= maxEmpty {
				e.log.Debug("ear: silence detected, ending listen (heard_speech=%v)", hasHeardSpeech)
				goto done
			}
			continue
		}

		emptyRuns = 0
		hasHeardSpeech = true

		// In case the user repeats the wake word mid-sentence, strip it.
		chunk = e.stripWakeWordClean(chunk)
		if chunk != "" {
			e.log.Debug("ear/listen: chunk: %q", chunk)
			parts = append(parts, chunk)
		}
	}

done:
	e.setState(earDormant)

	combined := strings.TrimSpace(strings.Join(parts, " "))
	if combined == "" {
		e.log.Debug("ear: listening ended with no input")
		return
	}

	e.log.Info("ear: heard command: %q", combined)

	select {
	case e.textCh <- combined:
	case <-ctx.Done():
	}
}

// ── Wake word matching ───────────────────────────────────────────

// stripWakeWord checks if the text contains a wake word.
// Returns the remaining text after the wake word, or "" if no wake
// word was found. A return of " " (space only) means the wake word
// was the entire utterance (or nothing meaningful followed it).
func (e *Ear) stripWakeWord(text string) string {
	lower := strings.ToLower(text)
	for _, w := range e.wakeWords {
		wl := strings.ToLower(w)

		// Exact match — wake word only, no command yet.
		if lower == wl {
			return " "
		}

		// Wake word at the start followed by more text.
		if strings.HasPrefix(lower, wl) {
			rest := strings.TrimSpace(text[len(wl):])
			rest = strings.TrimLeft(rest, " ,.\n\r\t")
			if rest == "" {
				return " "
			}
			return rest
		}

		// Wake word somewhere in the middle (e.g. "blah hey chef next").
		if idx := strings.Index(lower, wl); idx >= 0 {
			rest := strings.TrimSpace(text[idx+len(wl):])
			if rest == "" {
				return " "
			}
			return rest
		}
	}
	return ""
}

// stripWakeWordClean removes any wake word from the text and returns
// what's left. Used during active listening to clean mid-sentence
// repetitions.
func (e *Ear) stripWakeWordClean(text string) string {
	lower := strings.ToLower(text)
	for _, w := range e.wakeWords {
		wl := strings.ToLower(w)
		lower = strings.ReplaceAll(lower, wl, "")
	}
	return strings.TrimSpace(lower)
}

// isJustWakeWord returns true if the text is only whitespace or
// punctuation (i.e. the wake word was the entire utterance).
func isJustWakeWord(s string) bool {
	for _, r := range s {
		if r != ' ' && r != ',' && r != '.' && r != '!' && r != '?' {
			return false
		}
	}
	return true
}

// ── Recording ────────────────────────────────────────────────────

// recordChunk does one recording cycle with the given duration and
// returns the transcribed text.
func (e *Ear) recordChunk(ctx context.Context, duration time.Duration) string {
	var result string
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(text string) {
		result = text
		wg.Done()
	}

	verbose := e.log.GetLevel() >= logger.LevelVerbose
	t, err := audiotranscriber.NewTranscriber(
		e.whisperBin,
		e.modelPath,
		e.tempDir,
		"wav",
		callback,
		verbose,
	)
	if err != nil {
		e.log.Error("ear: transcriber init failed: %v", err)
		time.Sleep(2 * time.Second)
		return ""
	}

	if err := t.Start(); err != nil {
		e.log.Error("ear: recording start failed: %v", err)
		time.Sleep(2 * time.Second)
		return ""
	}

	select {
	case <-time.After(duration):
	case <-ctx.Done():
		t.Stop()
		wg.Wait()
		return ""
	}

	t.Stop()
	wg.Wait()

	return result
}

// ── Transcription cleanup ────────────────────────────────────────

// cleanTranscription strips whitespace, normalizes newlines, and
// removes common whisper artifacts like "[BLANK_AUDIO]", "(silence)",
// etc. Artifacts are stripped from anywhere in the text, not just as
// exact full-string matches.
func cleanTranscription(s string) string {
	// Normalize newlines and collapse whitespace.
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.TrimSpace(s)

	// Junk patterns to strip from anywhere in the text.
	junkPatterns := []string{
		"[BLANK_AUDIO]",
		"[BLANK AUDIO]",
		"(silence)",
		"[silence]",
		"(no speech)",
		"[no speech]",
		"[Music]",
		"(music)",
		"(keyboard clicking)",
		"(keyboard clacking)",
		"(typing)",
		"(clicking)",
		"(mouse clicking)",
		"(breathing)",
		"(sighing)",
		"(coughing)",
		"(laughing)",
		"(clapping)",
		"(footsteps)",
		"(door closing)",
		"(door opening)",
		"(knocking)",
		"(phone ringing)",
		"(birds chirping)",
		"(dog barking)",
		"(baby crying)",
		"(water running)",
		"(wind blowing)",
		"(rain)",
		"(thunder)",
		"(static)",
		"(background noise)",
		"(inaudible)",
		"(unintelligible)",
		"(applause)",
		"(cheering)",
		"(buzzing)",
		"(beeping)",
	}
	for _, j := range junkPatterns {
		s = strings.ReplaceAll(s, j, "")
		s = strings.ReplaceAll(s, strings.ToLower(j), "")
		s = strings.ReplaceAll(s, strings.ToUpper(j), "")
	}

	// Collapse any whitespace created by removals.
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)

	// Catch-all: strip any remaining (parenthesized) or [bracketed]
	// environmental annotations that whisper may produce, e.g.
	// "(dog barking)", "[laughter]", "(speaking French)", etc.
	s = envAnnotation.ReplaceAllString(s, "")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)

	// If what remains is just a known hallucination, discard entirely.
	hallucinations := []string{
		"...",
		"you",
		"Thank you.",
		"Thanks for watching!",
		"Thank you for watching.",
		"Bye.",
		"Bye!",
		"The end.",
		"Sous-titres réalisés para la communauté d'Amara.org",
	}
	lower := strings.ToLower(s)
	for _, h := range hallucinations {
		if strings.ToLower(h) == lower {
			return ""
		}
	}

	// Strip whisper timestamp prefixes like "[00:00:00.000 --> 00:00:05.000]"
	if strings.HasPrefix(s, "[") {
		if idx := strings.Index(s, "]"); idx != -1 && idx < 40 {
			rest := strings.TrimSpace(s[idx+1:])
			if rest != "" {
				return rest
			}
		}
	}

	return s
}
