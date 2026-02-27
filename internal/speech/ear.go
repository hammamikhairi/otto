package speech

import (
	"context"
	"math"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gordonklaus/portaudio"
	audiotranscriber "github.com/sklyt/whisper/pkg"

	"github.com/hammamikhairi/ottocook/internal/logger"
	"github.com/hammamikhairi/ottocook/internal/wakeword"
)

// earState represents the Ear's listening mode.
type earState int

const (
	// earDormant — waiting for the wakeword detector to fire.
	earDormant earState = iota
	// earListening — wake word detected, actively capturing the command.
	earListening
	// earMuted — ear is asleep while mouth speaks.
	earMuted
)

// EarState exports the ear state type for consumers (e.g. display).
type EarState = earState

// Exported constants for consumer code.
const (
	EarDormant   = earDormant
	EarListening = earListening
	EarMuted     = earMuted
)

// wakeWordTexts are patterns that may bleed into the whisper
// transcription if the tail of the wake-word utterance overlaps
// with the start of recording.  Used only for cleanup, not detection.
var wakeWordTexts = []string{
	"hey otto",
	"otto",
	"hey chef",
	"otto cook",
	"ottocook",
	"hey, chef",
	"hey shef",
}

// envAnnotation matches whisper environmental annotations like
// "(keyboard clicking)", "[laughter]", "(speaking French)", etc.
var envAnnotation = regexp.MustCompile(`[\(\[][a-zA-Z][a-zA-Z\s]*[\)\]]`)

// ── Options ──────────────────────────────────────────────────────

// EarOption configures the Ear.
type EarOption func(*Ear)

// WithTempDir sets the directory for temporary WAV files.
func WithTempDir(dir string) EarOption {
	return func(e *Ear) { e.tempDir = dir }
}

// WithListenTimeout sets how long the ear stays in active listening
// mode before giving up and returning to dormant.
func WithListenTimeout(d time.Duration) EarOption {
	return func(e *Ear) { e.listenTimeout = d }
}

// ── Ear ──────────────────────────────────────────────────────────

// Ear provides wake-word-triggered speech-to-text input.
//
// Lifecycle:
//  1. DORMANT — the openWakeWord ONNX detector runs continuously on
//     its own audio stream.  Zero whisper CPU during this phase.
//  2. LISTENING — detector fires → interrupt the Mouth → open a
//     single Whisper transcriber with RMS-based silence detection
//     → capture the full command → send text on the channel.
//  3. Return to dormant.
type Ear struct {
	whisperBin string
	modelPath  string
	tempDir    string
	log        *logger.Logger
	mouth      *Mouth             // optional — interrupt on wake word
	detector   *wakeword.Detector // ONNX-based wake word detector

	listenTimeout time.Duration // max active listening window

	mu            sync.Mutex
	muted         bool
	state         earState
	textCh        chan string          // transcribed text flows here
	wakeCh        chan struct{}        // wakeword detector signals here
	cancelCh      chan struct{}        // externally cancel active listening
	onStateChange func(state earState) // optional UI callback
}

// NewEar creates a wake-word-triggered voice input listener.
//
//   - whisperBin: path to the whisper-cli executable
//   - modelPath:  path to the Whisper GGML model file
//   - detector:   pre-configured openWakeWord detector
//   - mouth:      optional Mouth — will be interrupted when wake word is heard
func NewEar(whisperBin, modelPath string, detector *wakeword.Detector, mouth *Mouth, log *logger.Logger, opts ...EarOption) *Ear {
	e := &Ear{
		whisperBin:    whisperBin,
		modelPath:     modelPath,
		tempDir:       ".otto-stt",
		log:           log,
		mouth:         mouth,
		detector:      detector,
		listenTimeout: 15 * time.Second,
		state:         earDormant,
		textCh:        make(chan string, 8),
		wakeCh:        make(chan struct{}, 1),
		cancelCh:      make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(e)
	}

	// Validate that the whisper binary is reachable.
	if _, err := exec.LookPath(e.whisperBin); err != nil {
		log.Error("ear: whisper binary %q not found in PATH: %v", e.whisperBin, err)
	}

	// Wire the detector callback → wakeCh.
	detector.OnDetected = func() {
		select {
		case e.wakeCh <- struct{}{}:
		default: // already pending
		}
	}

	return e
}

// C returns the channel that receives transcribed text.
func (e *Ear) C() <-chan string {
	return e.textCh
}

// OnStateChange registers a callback invoked when the ear transitions
// between states (dormant, listening, muted).
func (e *Ear) OnStateChange(fn func(state earState)) {
	e.mu.Lock()
	e.onStateChange = fn
	e.mu.Unlock()
}

// Mute temporarily disables listening (e.g. during TTS playback).
// Also pauses the wakeword detector so it doesn't fire on speaker
// output.
func (e *Ear) Mute() {
	e.mu.Lock()
	e.muted = true
	curState := e.state
	e.mu.Unlock()
	e.detector.Pause()
	// Don't clobber earListening — the filler might trigger
	// OnSpeakingChange(true) → Mute while we're already listening.
	if curState != earListening {
		e.setState(earMuted)
	}
	e.log.Debug("ear: muted (state=%d)", curState)
}

// CancelListening aborts an in-progress listening session (if any)
// and returns the ear to dormant. Safe to call from any goroutine.
func (e *Ear) CancelListening() {
	if e.getState() != earListening {
		return
	}
	select {
	case e.cancelCh <- struct{}{}:
		e.log.Debug("ear: listening cancelled by user")
	default:
	}
}

// Unmute re-enables listening and resumes the wakeword detector.
func (e *Ear) Unmute() {
	e.mu.Lock()
	e.muted = false
	// Don't clobber earListening — if doListening is active, we must not
	// reset to dormant just because the mouth finished a filler line.
	curState := e.state
	e.mu.Unlock()
	if curState != earListening {
		e.detector.Resume()
		e.setState(earDormant)
	}
	e.log.Debug("ear: unmuted (state=%d)", curState)
}

func (e *Ear) isMuted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.muted
}

// Run starts the ear.  Blocks until ctx is cancelled.  The wakeword
// detector must already be running in its own goroutine.
func (e *Ear) Run(ctx context.Context) {
	e.log.Info("ear: started (timeout=%s)", e.listenTimeout)

	// Initialise PortAudio once for the lifetime of the ear.
	// Repeated Init/Terminate cycles corrupt the CoreAudio HAL on
	// macOS, progressively reducing the gain seen by the concurrent
	// malgo capture device.
	if err := portaudio.Initialize(); err != nil {
		e.log.Error("ear: portaudio init failed: %v", err)
		return
	}
	defer portaudio.Terminate()
	e.log.Debug("ear: portaudio initialized (once)")

	for {
		select {
		case <-ctx.Done():
			e.log.Info("ear: stopped")
			return

		case <-e.wakeCh:
			if e.isMuted() {
				e.log.Debug("ear: wake word ignored — muted")
				continue
			}
			e.onWakeWord(ctx)
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
	cb := e.onStateChange
	e.mu.Unlock()
	if cb != nil {
		cb(s)
	}
}

// waitForMouth blocks until the mouth finishes speaking so the
// microphone doesn't pick it up.
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

// ── Wake word handling ───────────────────────────────────────────

// onWakeWord is called when the ONNX detector fires.
func (e *Ear) onWakeWord(ctx context.Context) {
	e.log.Info("ear: wake word detected!")

	// Interrupt the mouth so it shuts up immediately.
	if e.mouth != nil {
		e.mouth.Interrupt()
		e.log.Debug("ear: interrupted mouth")
	}

	// Pause the wakeword detector while we listen — we don't want it
	// fighting over the mic or re-triggering on echoed audio.
	e.detector.Pause()

	// Mark listening BEFORE the filler so that OnSpeakingChange
	// callbacks (Mute/Unmute) know not to clobber this state.
	e.setState(earListening)

	// Speak a filler so the user knows we're listening.
	if e.mouth != nil {
		filler := LineListening()
		e.mouth.Say(filler, PriorityCritical)
		e.log.Debug("ear: said %q", filler)
	}
	sent := e.doListening(ctx)

	if sent {
		// Text was captured → an AI response is coming.  Mute so the
		// detector stays quiet during TTS.  The OnSpeakingChange callback
		// (mouth done → Unmute) will resume detection naturally.
		e.Mute()
	} else {
		// Nothing captured.  No AI response coming, so just resume the
		// detector directly (if not already muted by another path).
		if !e.isMuted() {
			e.detector.Resume()
		}
		e.setState(earDormant)
	}
}

// ── Active listening mode ────────────────────────────────────────

// doListening opens a single Whisper transcriber for the whole session
// (mic acquired once, released once) and runs a lightweight PortAudio
// monitor alongside it to measure RMS audio intensity.  The monitor
// decides when the user has stopped talking: 4 continuous seconds of
// silence after speech → done.  The transcriber's internal chunking
// handles mid-sentence pauses just fine; we only control the outer
// "are you done talking?" boundary.
//
// Returns true if transcribed text was sent on textCh.
func (e *Ear) doListening(ctx context.Context) bool {
	e.log.Info("ear: listening...")

	// Grace period: wait for the mouth to finish saying the filler
	// and give the user a moment to start speaking.
	e.waitForMouth(ctx)
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		e.setState(earDormant)
		return false
	}

	// ── RMS monitor stream ───────────────────────────────────────
	const (
		monSampleRate = 16000
		monFrames     = 1024
		rmsThresh     = 0.008 // below this = silence (≈ −42 dB)
		silenceDur    = 4 * time.Second
		graceDur      = 10 * time.Second // max wait before any speech
	)

	monBuf := make([]float32, monFrames)
	monStream, err := portaudio.OpenDefaultStream(1, 0, float64(monSampleRate), monFrames, monBuf)
	if err != nil {
		e.log.Error("ear: monitor stream open failed: %v", err)
		e.setState(earDormant)
		return false
	}
	if err := monStream.Start(); err != nil {
		e.log.Error("ear: monitor stream start failed: %v", err)
		monStream.Close()
		e.setState(earDormant)
		return false
	}

	// ── Whisper transcriber (single instance for the session) ────
	var result string
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(text string) {
		result = text
		wg.Done()
	}

	verbose := e.log.GetLevel() >= logger.LevelVerbose
	t, err := audiotranscriber.NewTranscriber(
		e.whisperBin, e.modelPath, e.tempDir, "wav", callback, verbose,
	)
	if err != nil {
		e.log.Error("ear: transcriber init failed: %v", err)
		monStream.Stop()
		monStream.Close()
		e.setState(earDormant)
		return false
	}
	if err := t.Start(); err != nil {
		e.log.Error("ear: recording start failed: %v", err)
		monStream.Stop()
		monStream.Close()
		e.setState(earDormant)
		return false
	}

	// ── Monitor loop ─────────────────────────────────────────────
	deadline := time.After(e.listenTimeout)
	lastLoud := time.Now()
	heardSpeech := false

	for {
		select {
		case <-ctx.Done():
			goto cleanup
		case <-deadline:
			e.log.Debug("ear: listen timeout reached")
			goto cleanup
		case <-e.cancelCh:
			e.log.Debug("ear: listening cancelled")
			goto cleanup
		default:
		}

		if err := monStream.Read(); err != nil {
			e.log.Debug("ear: monitor read error: %v", err)
			goto cleanup
		}

		var sumSq float64
		for _, s := range monBuf {
			sumSq += float64(s) * float64(s)
		}
		rms := math.Sqrt(sumSq / float64(len(monBuf)))

		// Ignore audio while the mouth is speaking — otherwise TTS
		// playback bleeds into the mic and gets treated as user speech.
		if e.mouth != nil && e.mouth.IsSpeaking() {
			continue
		}

		if rms >= rmsThresh {
			lastLoud = time.Now()
			if !heardSpeech {
				heardSpeech = true
				e.log.Debug("ear: speech detected (rms=%.4f)", rms)
			}
		}

		if heardSpeech && time.Since(lastLoud) >= silenceDur {
			e.log.Debug("ear: %.0fs silence after speech — done listening", silenceDur.Seconds())
			goto cleanup
		}

		if !heardSpeech && time.Since(lastLoud) >= graceDur {
			e.log.Debug("ear: no speech within grace period")
			goto cleanup
		}
	}

cleanup:
	monStream.Stop()
	monStream.Close()

	t.Stop()
	wg.Wait()

	e.setState(earDormant)

	combined := strings.TrimSpace(result)
	combined = cleanTranscription(combined)
	combined = stripWakeWordText(combined)
	combined = e.stripMouthEcho(combined)
	combined = strings.TrimSpace(combined)

	if combined == "" {
		e.log.Debug("ear: listening ended with no input")
		return false
	}

	e.log.Info("ear: heard command: %q", combined)

	select {
	case e.textCh <- combined:
		return true
	case <-ctx.Done():
		return false
	}
}

// ── Text cleanup ─────────────────────────────────────────────────

// stripMouthEcho removes text that matches what the mouth recently
// spoke — prevents the STT from feeding back TTS output as a command.
func (e *Ear) stripMouthEcho(text string) string {
	if e.mouth == nil {
		return text
	}
	last := e.mouth.LastSpoken()
	if last == "" {
		return text
	}
	lower := strings.ToLower(text)
	lastLower := strings.ToLower(last)
	if strings.Contains(lower, lastLower) {
		cleaned := strings.ReplaceAll(lower, lastLower, "")
		e.log.Debug("ear: stripped mouth echo from transcription")
		return strings.TrimSpace(cleaned)
	}
	return text
}

// stripWakeWordText removes any wake-word text fragments that may
// bleed into the whisper transcription.
func stripWakeWordText(text string) string {
	lower := strings.ToLower(text)
	for _, w := range wakeWordTexts {
		lower = strings.ReplaceAll(lower, w, "")
	}
	return strings.TrimSpace(lower)
}

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
	// environmental annotations that whisper may produce.
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
