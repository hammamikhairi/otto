// Package display provides the terminal UI using Bubble Tea.
//
// The [UI] type manages a persistent timer status bar and an input
// prompt at the bottom of the terminal. All application output is
// printed above the rendered area via Program.Println / Printf,
// ensuring concurrent writes never garble the display.
package display

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hammamikhairi/ottocook/internal/domain"
)

// ── Styles ───────────────────────────────────────────────────────

var (
	barBg = lipgloss.NewStyle().
		Background(lipgloss.Color("#27272a")).
		Foreground(lipgloss.Color("#a1a1aa"))

	timerRunStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#fde68a"))

	timerDoneStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#fca5a5"))

	timerPendingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#71717a")).
				Italic(true)

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#a1a1aa"))

	sepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#52525b"))

	promptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#94a3b8"))

	// ── Output styles (soft palette) ──

	// BannerStyle — muted slate for the startup banner.
	BannerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#94a3b8"))

	// Chat — soft sky blue for assistant speech.
	chatStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#bae6fd"))

	// Step — soft mint for step headers.
	stepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#bbf7d0"))

	// Primary text — light zinc for instructions.
	primaryStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d4d4d8"))

	// Secondary text — dimmed zinc for hints, tips, metadata.
	secondaryStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#71717a"))

	// Urgent — soft coral for errors/alerts.
	urgentOutputStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#fca5a5"))

	userInputEchoStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#a1a1aa"))

	sepLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3f3f46"))

	// ── Diff styles ──

	diffAddedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4ade80")) // green

	diffRemovedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#f87171")). // red
				Strikethrough(true)

	diffChangedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#fde68a")) // amber

	diffUnchangedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#71717a")) // dim

	// ── Inspector box styles ──

	inspectBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3f3f46")).
			Padding(0, 1).
			Width(36)

	inspectHeader = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#52525b")).
			Bold(true)

	inspectLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#71717a"))

	inspectOn = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4ade80")) // green

	inspectActive = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#fde68a")) // amber

	inspectDim = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#52525b")) // dim

	inspectOff = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#52525b")).
			Italic(true)

	inspectTimer = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#a1a1aa"))

	brandStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#52525b")).
			Bold(true)
)

// ── UI ───────────────────────────────────────────────────────────

// UI manages the terminal through Bubble Tea.
//
// Call [NewUI] then [UI.Run] (blocking).  Other goroutines may
// safely call [UI.Println], [UI.Printf], and read from
// [UI.InputChan] at any time after [UI.WaitReady] returns.
type UI struct {
	program     *tea.Program
	inputCh     chan string
	readyCh     chan struct{}
	quitCh      chan struct{}
	store       domain.SessionStore
	done        atomic.Bool
	interruptFn func() // called when user presses space on empty input

	// Ear timing constants passed in once at startup.
	earListenTimeout time.Duration
	earSilenceDur    time.Duration
	earGraceDur      time.Duration
}

// SetEarTimingConstants stores the ear's timing parameters so the
// inspector can show countdowns.  Call before Run().
func (u *UI) SetEarTimingConstants(listenTimeout, silenceDur, graceDur time.Duration) {
	u.earListenTimeout = listenTimeout
	u.earSilenceDur = silenceDur
	u.earGraceDur = graceDur
}

// SetEarState updates the ear indicator in the inspector box. Thread-safe.
func (u *UI) SetEarState(s EarIndicator) {
	if u.program != nil && !u.done.Load() {
		u.program.Send(earStateMsg{state: s})
	}
}

// SetMouthState updates the mouth indicator in the inspector box. Thread-safe.
func (u *UI) SetMouthState(s MouthIndicator) {
	if u.program != nil && !u.done.Load() {
		u.program.Send(mouthStateMsg{state: s})
	}
}

// OnInterrupt registers a callback invoked when the user presses
// space with an empty input line (i.e. "shut up" gesture).
func (u *UI) OnInterrupt(fn func()) { u.interruptFn = fn }

// NewUI creates the display. Call Run() to start.
func NewUI(store domain.SessionStore) *UI {
	return &UI{
		store:   store,
		inputCh: make(chan string, 16),
		readyCh: make(chan struct{}),
		quitCh:  make(chan struct{}),
	}
}

// Println appends a line to the message buffer. Thread-safe.
func (u *UI) Println(a ...interface{}) {
	text := fmt.Sprint(a...)
	if u.program != nil && !u.done.Load() {
		u.program.Send(appendMsg{text: text})
	} else {
		fmt.Println(text)
	}
}

// Printf appends formatted text to the message buffer. Thread-safe.
func (u *UI) Printf(format string, a ...interface{}) {
	text := strings.TrimRight(fmt.Sprintf(format, a...), "\n")
	if u.program != nil && !u.done.Load() {
		u.program.Send(appendMsg{text: text})
	} else {
		fmt.Printf(format, a...)
	}
}

// InputChan returns completed user-input lines.
func (u *UI) InputChan() <-chan string { return u.inputCh }

// ── Styled print helpers ─────────────────────────────────────────
// These give output visual hierarchy with lipgloss colors.

// PrintChat prints a conversational assistant line with a typewriter effect.
func (u *UI) PrintChat(text string) {
	if u.program != nil && !u.done.Load() {
		u.program.Send(typewriterStartMsg{text: text, style: chatStyle})
		return
	}
	u.Println(chatStyle.Render("  " + text))
}

// PrintStep prints a step header like "Step 2/8 (~5m)".
func (u *UI) PrintStep(text string) {
	u.Println(stepStyle.Render("  " + text))
}

// PrintInstruction prints the step's main instruction text.
func (u *UI) PrintInstruction(text string) {
	u.Println(primaryStyle.Render("  " + text))
}

// PrintHint prints a secondary/dimmed line.
func (u *UI) PrintHint(text string) {
	u.Println(secondaryStyle.Render("  " + text))
}

// PrintUrgent prints an urgent/error line (red, bold).
func (u *UI) PrintUrgent(text string) {
	u.Println(urgentOutputStyle.Render("  " + text))
}

// PrintDiffAdded prints a "+" prefixed line in green.
func (u *UI) PrintDiffAdded(text string) {
	u.Println(diffAddedStyle.Render("  + " + text))
}

// PrintDiffRemoved prints a "-" prefixed line in red with strikethrough.
func (u *UI) PrintDiffRemoved(text string) {
	u.Println(diffRemovedStyle.Render("  - " + text))
}

// PrintDiffChanged prints a "~" prefixed line in amber.
func (u *UI) PrintDiffChanged(text string) {
	u.Println(diffChangedStyle.Render("  ~ " + text))
}

// PrintDiffUnchanged prints a dim unchanged context line.
func (u *UI) PrintDiffUnchanged(text string) {
	u.Println(diffUnchangedStyle.Render("    " + text))
}

// PrintVoice prints a voice-recognised input line.
func (u *UI) PrintVoice(text string) {
	if u.program != nil && !u.done.Load() {
		u.program.Send(voiceInputEchoMsg{text: text})
		return
	}
	fmt.Println("otto> [heard] " + text)
}

// PrintUserInput echoes the user's typed command into the scrollback.
func (u *UI) PrintUserInput(text string) {
	if u.program != nil && !u.done.Load() {
		u.program.Send(userInputEchoMsg{text: text})
		return
	}
	fmt.Println("otto> " + text)
}

// SetActivity shows an animated spinner with the given label above the
// input prompt. Thread-safe. Call ClearActivity to remove it.
func (u *UI) SetActivity(label string) {
	if u.program != nil && !u.done.Load() {
		u.program.Send(activityMsg{label: label})
	}
}

// ClearActivity hides the activity spinner. Thread-safe.
func (u *UI) ClearActivity() {
	if u.program != nil && !u.done.Load() {
		u.program.Send(activityMsg{label: ""})
	}
}

// WaitReady blocks until the Bubble Tea event loop is running.
func (u *UI) WaitReady() { <-u.readyCh }

// Quit tells Bubble Tea to exit.
func (u *UI) Quit() {
	if u.program != nil {
		u.program.Quit()
	}
}

// QuitChan is closed when Run returns.
func (u *UI) QuitChan() <-chan struct{} { return u.quitCh }

// Run starts the Bubble Tea event loop.  Blocks until quit.
func (u *UI) Run() error {
	ti := textinput.New()
	// Use a plain-text prompt so the textinput width math stays correct.
	// Lipgloss-styled prompts add invisible ANSI bytes that break the
	// internal offset/scroll calculations for long input.
	ti.Prompt = "otto> "
	ti.PromptStyle = promptStyle
	ti.TextStyle = userInputEchoStyle
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#94a3b8"))
	ti.Focus()
	ti.CharLimit = 500
	ti.Width = 60 // updated on first WindowSizeMsg

	m := model{
		store:            u.store,
		input:            ti,
		inputCh:          u.inputCh,
		readyCh:          u.readyCh,
		interruptFn:      u.interruptFn,
		earListenTimeout: u.earListenTimeout,
		earSilenceDur:    u.earSilenceDur,
		earGraceDur:      u.earGraceDur,
	}

	u.program = tea.NewProgram(m, tea.WithAltScreen())
	_, err := u.program.Run()
	u.done.Store(true)
	close(u.quitCh)
	return err
}

// Teardown is an alias for Quit kept for drop-in compatibility.
func (u *UI) Teardown() { u.Quit() }

// ── Bubble Tea model ─────────────────────────────────────────────

type model struct {
	store       domain.SessionStore
	input       textinput.Model
	inputCh     chan<- string
	readyCh     chan struct{}
	interruptFn func() // called on space-when-empty ("shut up")
	timers      []timerInfo
	width       int
	height      int

	// Message buffer — all output goes here instead of program.Println.
	messages []string

	// Typewriter state.
	twLines   []string       // pre-wrapped lines of plain text still to reveal
	twCurLine int            // index into twLines for current line
	twCurPos  int            // runes revealed on current line
	twStyle   lipgloss.Style // style applied to the line

	// Activity spinner state.
	activityLabel string // e.g. "Thinking" — empty means no spinner
	activityFrame int    // index into spinner frames
	activityGen   int    // generation counter — stale ticks are dropped

	// Inspector box state.
	earState        EarIndicator
	earActiveSince  time.Time // when ear entered EarActive
	mouthState      MouthIndicator
	mouthSpeakSince time.Time // when mouth started speaking

	// Ear timing constants (set once at init).
	earListenTimeout time.Duration
	earSilenceDur    time.Duration
	earGraceDur      time.Duration
}

type timerInfo struct {
	label     string
	remaining time.Duration
	fired     bool
	pending   bool
}

// Messages.
type tickMsg time.Time

// userInputEchoMsg wraps typed user input into the scrollback with line wrapping.
type userInputEchoMsg struct{ text string }

// voiceInputEchoMsg wraps voice-recognised input into the scrollback with line wrapping.
type voiceInputEchoMsg struct{ text string }

// typewriterStartMsg begins a new typewriter line.
type typewriterStartMsg struct {
	text  string         // plain text to reveal
	style lipgloss.Style // style to render with
}

// typewriterTickMsg advances the typewriter by one chunk.
type typewriterTickMsg struct{}

// appendMsg adds a line to the message buffer (replaces program.Println).
type appendMsg struct {
	text string
}

// activityMsg sets or clears the activity spinner.
type activityMsg struct {
	label string // empty = clear
}

// EarIndicator represents the ear's display state.
type EarIndicator int

const (
	EarOff      EarIndicator = iota // no voice mode
	EarReady                        // waiting for wake word
	EarActive                       // actively listening
	EarSleeping                     // muted while mouth speaks
)

// MouthIndicator represents the mouth's display state.
type MouthIndicator int

const (
	MouthOff      MouthIndicator = iota // TTS disabled
	MouthIdle                           // ready, not speaking
	MouthSpeaking                       // actively playing audio
)

// earStateMsg carries a state change for the ear indicator.
type earStateMsg struct {
	state EarIndicator
}

// mouthStateMsg carries a state change for the mouth indicator.
type mouthStateMsg struct {
	state MouthIndicator
}

// activityTickMsg advances the spinner animation.
type activityTickMsg struct {
	gen int
}

// Spinner frames.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var activityStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#fde68a"))

// Activity crossing-bar colors (warm amber family, progressively dimmer).
var (
	actBarHi  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fde68a"))
	actBarMid = lipgloss.NewStyle().Foreground(lipgloss.Color("#b8943d"))
	actBarLo  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7a6228"))
	actBarDim = lipgloss.NewStyle().Foreground(lipgloss.Color("#4a3b18"))
)

func (m model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		tickCmd(),
		signalReady(m.readyCh),
	)
}

func signalReady(ch chan struct{}) tea.Cmd {
	return func() tea.Msg {
		close(ch)
		return nil
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeySpace:
			if m.input.Value() == "" && m.interruptFn != nil {
				m.interruptFn()
				return m, nil
			}
		case tea.KeyEnter:
			v := m.input.Value()
			m.input.Reset()
			if strings.TrimSpace(v) != "" {
				m.inputCh <- v
				return m, func() tea.Msg {
					return userInputEchoMsg{text: v}
				}
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		const promptLen = 6
		if msg.Width > promptLen+1 {
			m.input.Width = msg.Width - promptLen - 1
		}
		return m, nil

	case tickMsg:
		m.refreshTimers()
		cmds := []tea.Cmd{tickCmd()}
		if len(m.timers) > 0 {
			cmds = append(cmds, tea.SetWindowTitle(m.titleStr()))
		} else {
			cmds = append(cmds, tea.SetWindowTitle("OttoCook"))
		}
		return m, tea.Batch(cmds...)

	case typewriterStartMsg:
		// Flush any in-progress typewriter lines directly to messages.
		if len(m.twLines) > 0 {
			for i := m.twCurLine; i < len(m.twLines); i++ {
				m.messages = append(m.messages, m.twStyle.Render("  "+m.twLines[i]))
			}
		}
		// Pre-wrap text into terminal-width lines.
		w := m.width
		if w <= 0 {
			w = 80
		}
		const indent = 2 // "  " prefix
		m.twLines = wrapText(msg.text, w-indent)
		m.twStyle = msg.style
		m.twCurLine = 0
		m.twCurPos = 0
		return m, twTickCmd()

	case typewriterTickMsg:
		if len(m.twLines) == 0 || m.twCurLine >= len(m.twLines) {
			return m, nil
		}
		chunk := 2
		m.twCurPos += chunk
		curRunes := []rune(m.twLines[m.twCurLine])
		if m.twCurPos >= len(curRunes) {
			// Current line done — commit to message buffer.
			finishedLine := m.twStyle.Render("  " + m.twLines[m.twCurLine])
			m.messages = append(m.messages, finishedLine)
			m.twCurLine++
			m.twCurPos = 0

			if m.twCurLine >= len(m.twLines) {
				m.twLines = nil
				return m, nil
			}
			return m, twTickCmd()
		}
		return m, twTickCmd()

	case activityMsg:
		m.activityLabel = msg.label
		m.activityFrame = 0
		m.activityGen++
		if msg.label != "" {
			return m, activityTickCmd(m.activityGen)
		}
		return m, nil

	case activityTickMsg:
		if m.activityLabel == "" || msg.gen != m.activityGen {
			return m, nil
		}
		m.activityFrame++
		return m, activityTickCmd(m.activityGen)

	case earStateMsg:
		if msg.state == EarActive && m.earState != EarActive {
			m.earActiveSince = time.Now()
		}
		if msg.state != EarActive {
			m.earActiveSince = time.Time{}
		}
		m.earState = msg.state
		return m, nil

	case mouthStateMsg:
		if msg.state == MouthSpeaking && m.mouthState != MouthSpeaking {
			m.mouthSpeakSince = time.Now()
		}
		if msg.state != MouthSpeaking {
			m.mouthSpeakSince = time.Time{}
		}
		m.mouthState = msg.state
		return m, nil

	case userInputEchoMsg:
		w := m.width
		if w <= 0 {
			w = 80
		}
		sep := sepLineStyle.Render("  " + strings.Repeat("╌", 46))
		m.messages = append(m.messages, sep)
		prefix := promptStyle.Render("otto") + secondaryStyle.Render("> ")
		prefixW := lipgloss.Width(prefix)
		wrapped := wrapText(msg.text, w-prefixW)
		for i, line := range wrapped {
			if i == 0 {
				m.messages = append(m.messages, prefix+userInputEchoStyle.Render(line))
			} else {
				m.messages = append(m.messages, strings.Repeat(" ", prefixW)+userInputEchoStyle.Render(line))
			}
		}
		return m, nil

	case voiceInputEchoMsg:
		w := m.width
		if w <= 0 {
			w = 80
		}
		sep := sepLineStyle.Render("  " + strings.Repeat("╌", 46))
		m.messages = append(m.messages, sep)
		prefix := secondaryStyle.Render("otto> [heard] ")
		prefixW := lipgloss.Width(prefix)
		wrapped := wrapText(msg.text, w-prefixW)
		for i, line := range wrapped {
			if i == 0 {
				m.messages = append(m.messages, prefix+primaryStyle.Render(line))
			} else {
				m.messages = append(m.messages, strings.Repeat(" ", prefixW)+primaryStyle.Render(line))
			}
		}
		return m, nil

	case appendMsg:
		m.messages = append(m.messages, msg.text)
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// twTickCmd schedules the next typewriter tick.
func twTickCmd() tea.Cmd {
	return tea.Tick(25*time.Millisecond, func(time.Time) tea.Msg {
		return typewriterTickMsg{}
	})
}

// activityTickCmd schedules the next spinner frame.
func activityTickCmd(gen int) tea.Cmd {
	return tea.Tick(32*time.Millisecond, func(time.Time) tea.Msg {
		return activityTickMsg{gen: gen}
	})
}

// crossingBar renders a dashed underline with two glowing spots
// traveling in opposite directions.
func crossingBar(frame, width int) string {
	var b strings.Builder
	fw := float64(width)
	for x := 0; x < width; x++ {
		pos1 := math.Mod(float64(frame)*0.4, fw)
		pos2 := fw - math.Mod(float64(frame)*0.3, fw)
		d1 := math.Abs(float64(x) - pos1)
		d2 := math.Abs(float64(x) - pos2)
		dist := math.Min(d1, d2)
		var st lipgloss.Style
		switch {
		case dist < 2:
			st = actBarHi
		case dist < 4:
			st = actBarMid
		case dist < 7:
			st = actBarLo
		default:
			st = actBarDim
		}
		b.WriteString(st.Render("╌"))
	}
	return b.String()
}

// wrapText breaks s into lines of at most maxWidth runes, splitting
// on word boundaries when possible.
func wrapText(s string, maxWidth int) []string {
	if maxWidth <= 0 {
		maxWidth = 78
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{s}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if len([]rune(cur))+1+len([]rune(w)) > maxWidth {
			lines = append(lines, cur)
			cur = w
		} else {
			cur += " " + w
		}
	}
	lines = append(lines, cur)
	return lines
}

func (m *model) refreshTimers() {
	sessions, err := m.store.ListActive(context.Background())
	if err != nil {
		return
	}
	m.timers = m.timers[:0]
	for _, s := range sessions {
		for _, ts := range s.TimerStates {
			switch ts.Status {
			case domain.TimerPending:
				m.timers = append(m.timers, timerInfo{
					label:     ts.Label,
					remaining: ts.Remaining,
					pending:   true,
				})
			case domain.TimerRunning:
				m.timers = append(m.timers, timerInfo{
					label:     ts.Label,
					remaining: ts.Remaining,
				})
			case domain.TimerFired:
				m.timers = append(m.timers, timerInfo{
					label: ts.Label,
					fired: true,
				})
			}
		}
	}
	// Sort by label so the bar doesn't shuffle every tick.
	sort.Slice(m.timers, func(i, j int) bool {
		return m.timers[i].label < m.timers[j].label
	})
}

func (m model) titleStr() string {
	var p []string
	for _, t := range m.timers {
		if t.fired {
			p = append(p, t.label+": DONE!")
		} else if t.pending {
			p = append(p, t.label+": waiting")
		} else {
			p = append(p, t.label+": "+fmtDuration(t.remaining))
		}
	}
	return "OttoCook — " + strings.Join(p, " | ")
}

func (m model) View() string {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	// ── 1. Top row: branding left + inspector right ──
	var topLines []string
	box := m.renderInspector()
	brand := brandStyle.Render("  Otto")
	if box != "" {
		// Place brand left, inspector right on the same rows.
		boxLines := strings.Split(box, "\n")
		boxW := lipgloss.Width(box)
		brandW := lipgloss.Width(brand)
		for i, bl := range boxLines {
			left := ""
			if i == 0 {
				left = brand
			}
			gap := w - brandW - boxW
			if i != 0 {
				gap = w - boxW
			}
			if gap < 0 {
				gap = 0
			}
			topLines = append(topLines, left+strings.Repeat(" ", gap)+bl)
		}
	} else {
		topLines = append(topLines, brand)
	}

	// ── 2. Timer bar (pinned right after top row) ──
	if len(m.timers) > 0 {
		topLines = append(topLines, m.renderBar())
		topLines = append(topLines, "") // buffer line
	}

	// ── 3. Bottom section: activity + typewriter + blank + prompt ──
	var bottomParts []string
	if m.activityLabel != "" {
		frame := spinnerFrames[m.activityFrame%len(spinnerFrames)]
		bottomParts = append(bottomParts,
			activityStyle.Render("  "+frame+" "+m.activityLabel))
		barW := 1 + 1 + len([]rune(m.activityLabel))
		bottomParts = append(bottomParts,
			"  "+crossingBar(m.activityFrame, barW))
	}
	if len(m.twLines) > 0 && m.twCurLine < len(m.twLines) {
		runes := []rune(m.twLines[m.twCurLine])
		n := m.twCurPos
		if n > len(runes) {
			n = len(runes)
		}
		bottomParts = append(bottomParts,
			m.twStyle.Render("  "+string(runes[:n])))
	}
	bottomParts = append(bottomParts, "") // blank separator
	bottomParts = append(bottomParts, m.input.View())

	// ── 4. Message area fills remaining height ──
	topH := len(topLines)
	bottomH := len(bottomParts)
	msgH := h - topH - bottomH
	if msgH < 0 {
		msgH = 0
	}

	// ── 5. Compose full screen ──
	var out []string
	out = append(out, topLines...)
	out = append(out, m.renderMessages(msgH)...)
	out = append(out, bottomParts...)

	return strings.Join(out, "\n")
}

func (m model) renderBar() string {
	var parts []string
	for _, t := range m.timers {
		if t.fired {
			parts = append(parts, timerDoneStyle.Render(t.label+": DONE!"))
		} else if t.pending {
			parts = append(parts, timerPendingStyle.Render(t.label+": waiting"))
		} else {
			parts = append(parts,
				labelStyle.Render(t.label+": ")+
					timerRunStyle.Render(fmtDuration(t.remaining)))
		}
	}

	content := " " + strings.Join(parts, sepStyle.Render("  │  ")) + " "

	w := m.width
	if w <= 0 {
		w = 80
	}
	return barBg.Width(w).Render(content)
}

// renderMessages returns exactly `height` lines from the tail of the
// message buffer, padding with blanks at top when content is short.
func (m model) renderMessages(height int) []string {
	if height <= 0 {
		return nil
	}

	// Flatten all messages into individual terminal lines.
	var allLines []string
	for _, msg := range m.messages {
		allLines = append(allLines, strings.Split(msg, "\n")...)
	}

	// Take the last `height` lines.
	start := len(allLines) - height
	if start < 0 {
		start = 0
	}
	visible := allLines[start:]

	// Pad with blank lines at the top.
	for len(visible) < height {
		visible = append([]string{""}, visible...)
	}

	return visible
}

// renderInspector builds the top-right status box showing ear + mouth state.
func (m model) renderInspector() string {
	if m.earState == EarOff && m.mouthState == MouthOff {
		return ""
	}

	// Inner content width = box Width - 2 (border) - 2 (padding).
	const innerW = 32

	// row renders "label" flush-left and "value" flush-right within innerW.
	row := func(label, value string) string {
		labelW := lipgloss.Width(label)
		valueW := lipgloss.Width(value)
		gap := innerW - labelW - valueW
		if gap < 1 {
			gap = 1
		}
		return label + strings.Repeat(" ", gap) + value
	}

	var lines []string
	lines = append(lines, inspectHeader.Render("-- status --"))

	// ── Ear ──
	switch m.earState {
	case EarReady:
		lines = append(lines, row(
			inspectLabel.Render("ear"),
			inspectOn.Render("awaiting wake word")))
	case EarActive:
		elapsed := m.fmtElapsed(m.earActiveSince)
		lines = append(lines, row(
			inspectLabel.Render("ear"),
			inspectActive.Render("listening ")+inspectTimer.Render(elapsed)))
		if m.earListenTimeout > 0 && !m.earActiveSince.IsZero() {
			remain := m.earListenTimeout - time.Since(m.earActiveSince)
			if remain < 0 {
				remain = 0
			}
			lines = append(lines, row(
				inspectLabel.Render("└ timeout"),
				inspectTimer.Render(fmtDuration(remain))))
		}
	case EarSleeping:
		lines = append(lines, row(
			inspectLabel.Render("ear"),
			inspectDim.Render("paused")))
	default:
		lines = append(lines, row(
			inspectLabel.Render("ear"),
			inspectOff.Render("disabled")))
	}

	// ── Mouth ──
	switch m.mouthState {
	case MouthIdle:
		lines = append(lines, row(
			inspectLabel.Render("mouth"),
			inspectOn.Render("idle")))
	case MouthSpeaking:
		elapsed := m.fmtElapsed(m.mouthSpeakSince)
		lines = append(lines, row(
			inspectLabel.Render("mouth"),
			inspectActive.Render("speaking ")+inspectTimer.Render(elapsed)))
	default:
		lines = append(lines, row(
			inspectLabel.Render("mouth"),
			inspectOff.Render("disabled")))
	}

	content := strings.Join(lines, "\n")
	return inspectBorder.Render(content)
}

// fmtElapsed formats duration since t as a compact string.
func (m model) fmtElapsed(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t).Truncate(time.Second)
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// ── Helpers ──────────────────────────────────────────────────────

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if m == 0 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}
