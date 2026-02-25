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
)

// ── UI ───────────────────────────────────────────────────────────

// UI manages the terminal through Bubble Tea.
//
// Call [NewUI] then [UI.Run] (blocking).  Other goroutines may
// safely call [UI.Println], [UI.Printf], and read from
// [UI.InputChan] at any time after [UI.WaitReady] returns.
type UI struct {
	program *tea.Program
	inputCh chan string
	readyCh chan struct{}
	quitCh  chan struct{}
	store   domain.SessionStore
	done    atomic.Bool
}

// NewUI creates the display. Call Run() to start.
func NewUI(store domain.SessionStore) *UI {
	return &UI{
		store:   store,
		inputCh: make(chan string, 16),
		readyCh: make(chan struct{}),
		quitCh:  make(chan struct{}),
	}
}

// Println prints a line above the prompt. Thread-safe.
// Each argument is converted via fmt.Sprint and printed on its own
// line(s).  If the program hasn't started yet, falls back to
// fmt.Println.
func (u *UI) Println(a ...interface{}) {
	if u.program != nil && !u.done.Load() {
		u.program.Println(a...)
	} else {
		fmt.Println(a...)
	}
}

// Printf prints formatted text above the prompt. Thread-safe.
// The output is printed on its own line (a trailing newline in the
// format string will produce an extra blank line).
func (u *UI) Printf(format string, a ...interface{}) {
	if u.program != nil && !u.done.Load() {
		u.program.Printf(format, a...)
	} else {
		fmt.Printf(format, a...)
	}
}

// InputChan returns completed user-input lines.
func (u *UI) InputChan() <-chan string { return u.inputCh }

// ── Styled print helpers ─────────────────────────────────────────
// These give output visual hierarchy with lipgloss colors.

// PrintChat prints a conversational assistant line.
func (u *UI) PrintChat(text string) {
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

// PrintVoice prints a voice-recognised input line.
func (u *UI) PrintVoice(text string) {
	u.Println(secondaryStyle.Render("[voice] ") + primaryStyle.Render(text))
}

// PrintUserInput echoes the user's typed command into the scrollback.
func (u *UI) PrintUserInput(text string) {
	u.Println(promptStyle.Render("otto") + secondaryStyle.Render("> ") + userInputEchoStyle.Render(text))
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
		store:   u.store,
		input:   ti,
		inputCh: u.inputCh,
		readyCh: u.readyCh,
		echoFn: func(v string) {
			u.PrintUserInput(v)
		},
	}

	u.program = tea.NewProgram(m)
	_, err := u.program.Run()
	u.done.Store(true)
	close(u.quitCh)
	return err
}

// Teardown is an alias for Quit kept for drop-in compatibility.
func (u *UI) Teardown() { u.Quit() }

// ── Bubble Tea model ─────────────────────────────────────────────

type model struct {
	store   domain.SessionStore
	input   textinput.Model
	inputCh chan<- string
	readyCh chan struct{}
	echoFn  func(string) // prints user input into scrollback
	timers  []timerInfo
	width   int
}

type timerInfo struct {
	label     string
	remaining time.Duration
	fired     bool
	pending   bool
}

// Messages.
type tickMsg time.Time

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
		case tea.KeyEnter:
			v := m.input.Value()
			m.input.Reset()
			if strings.TrimSpace(v) != "" {
				m.inputCh <- v
				// Return a Cmd that prints the echo — this runs
				// outside Update so it won't deadlock on msgs.
				echoFn := m.echoFn
				return m, func() tea.Msg {
					echoFn(v)
					return nil
				}
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		// Let the text input use the full width minus the prompt ("otto> " = 6 chars).
		const promptLen = 6
		if msg.Width > promptLen {
			m.input.Width = msg.Width - promptLen
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
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
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
	var b strings.Builder

	if len(m.timers) > 0 {
		b.WriteString(m.renderBar())
		b.WriteByte('\n')
	}

	// Blank line before prompt for visual separation.
	b.WriteByte('\n')
	b.WriteString(m.input.View())
	return b.String()
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
