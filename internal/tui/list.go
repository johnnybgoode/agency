package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/johnnybgoode/agency/internal/session"
	"github.com/johnnybgoode/agency/internal/state"
)

// Lipgloss styles used across the list view and create form.
var (
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	runningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	failedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	pausedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	doneStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	pendingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// --- Messages ---

// tickMsg is emitted on the polling interval to reload session state.
type tickMsg struct{}

// sessionCreatedMsg is emitted after an async Create call completes.
type sessionCreatedMsg struct {
	err error
}

// sessionRemovedMsg is emitted after an async Remove call completes.
type sessionRemovedMsg struct {
	id  string
	err error
}

// reconcileDoneMsg is emitted after an async Reconcile call completes.
type reconcileDoneMsg struct {
	err error
}

// --- Model ---

// listModel is the top-level Bubble Tea model for the sidebar session list view.
type listModel struct {
	manager    *session.Manager
	sessions   []*state.Session
	cursor     int
	width      int
	height     int
	err        error
	confirming bool // inline delete confirm (shown in help area)
	confirmID  string
}

// newListModel constructs the list model, pre-populating the session list.
func newListModel(mgr *session.Manager) listModel {
	return listModel{
		manager:  mgr,
		sessions: mgr.List(),
	}
}

// Init returns the polling tick command.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) Init() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

// handleConfirmKey handles key presses when a delete confirmation is in progress.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) handleConfirmKey(msg tea.KeyMsg) (listModel, tea.Cmd) {
	switch msg.String() {
	case "y":
		id := m.confirmID
		m.confirming = false
		m.confirmID = ""
		mgr := m.manager
		return m, func() tea.Msg {
			err := mgr.Remove(context.Background(), id)
			return sessionRemovedMsg{id: id, err: err}
		}
	case "n", "esc":
		m.confirming = false
		m.confirmID = ""
	}
	return m, nil
}

// handleNormalKey handles key presses in normal (non-confirming) mode.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) handleNormalKey(msg tea.KeyMsg) (listModel, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "n":
		// Print hint to run agency new --popup; in popup mode the create form runs separately.
		fmt.Print("\r\n  Run: agency new --popup\r\n")
		return m, nil

	case "enter":
		if len(m.sessions) > 0 && m.cursor < len(m.sessions) {
			sess := m.sessions[m.cursor]
			activeID := m.manager.State.ActiveSessionID
			mainWindowID := m.manager.State.MainWindowID
			if sess.PaneID != "" && mainWindowID != "" {
				if activeID == sess.ID {
					// Already active — focus the pane by selecting the main window.
					_ = m.manager.Tmux.SelectWindow(mainWindowID)
				} else {
					// Join the session pane into the main window as the right pane.
					_ = m.manager.Tmux.JoinPane(sess.PaneID, mainWindowID)
					m.manager.State.ActiveSessionID = sess.ID
					_ = m.manager.SaveState()
				}
			} else if sess.TmuxWindow != "" {
				// Fallback: just select the window.
				_ = m.manager.Tmux.SelectWindow(sess.TmuxWindow)
			}
		}

	case "d":
		if len(m.sessions) > 0 && m.cursor < len(m.sessions) {
			m.confirming = true
			m.confirmID = m.sessions[m.cursor].ID
		}

	case "r":
		mgr := m.manager
		return m, func() tea.Msg {
			err := mgr.Reconcile(context.Background())
			return reconcileDoneMsg{err: err}
		}

	case "j", "down":
		if m.cursor < len(m.sessions)-1 {
			m.cursor++
		}

	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	}

	return m, nil
}

// Update handles all incoming messages and key presses.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle delete confirmation first.
	if m.confirming {
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.handleConfirmKey(key)
		}
		// Still schedule the next tick while confirming.
		if _, ok := msg.(tickMsg); ok {
			return m, tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg{} })
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tickMsg:
		// Reload sessions from state file on each tick.
		if s, err := state.Read(m.manager.StatePath); err == nil {
			m.manager.State = s
			m.sessions = m.manager.List()
			if m.cursor >= len(m.sessions) && len(m.sessions) > 0 {
				m.cursor = len(m.sessions) - 1
			}
		}
		return m, tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg{} })

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		return m.handleNormalKey(msg)

	case sessionCreatedMsg:
		m.sessions = m.manager.List()
		if msg.err != nil {
			m.err = friendlyError(msg.err)
		} else {
			m.err = nil
		}
		if m.cursor >= len(m.sessions) && len(m.sessions) > 0 {
			m.cursor = len(m.sessions) - 1
		}

	case sessionRemovedMsg:
		m.sessions = m.manager.List()
		if msg.err != nil {
			m.err = friendlyError(msg.err)
		} else {
			m.err = nil
		}
		if m.cursor >= len(m.sessions) && len(m.sessions) > 0 {
			m.cursor = len(m.sessions) - 1
		}

	case reconcileDoneMsg:
		m.sessions = m.manager.List()
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
		}
	}

	return m, nil
}

// sidebarWidth returns the effective sidebar total width (including the right │).
// Defaults to 22 if neither the config nor m.width are set.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) sidebarWidth() int {
	// Prefer the explicitly set terminal width if available.
	if m.width > 0 {
		return m.width
	}
	w := m.manager.Cfg.TUI.SidebarWidth
	if w <= 0 {
		w = 22
	}
	return w
}

// truncate shortens s to maxLen runes, appending ".." if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 2 {
		return s[:maxLen]
	}
	return string(runes[:maxLen-2]) + ".."
}

// View renders the sidebar TUI.
//
// Layout (w = sidebarWidth, inner = w-1):
//
//	───── Agency ────────╮   <- top rule with embedded title, right corner
//	                     │   <- content rows: inner cols + "│"
//	─────────────────────╯   <- bottom rule with right corner
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) View() string {
	w := m.sidebarWidth()
	// inner is the number of content columns before the right "│".
	inner := w - 1
	if inner < 1 {
		inner = 1
	}

	// row pads content to inner columns then appends the right border char.
	// It uses lipgloss.Width so that ANSI escape sequences are not counted.
	row := func(content string) string {
		visWidth := lipgloss.Width(content)
		if visWidth > inner {
			// Hard-truncate visible characters — strip styled content to fit.
			content = truncate(content, inner)
			visWidth = lipgloss.Width(content)
		}
		pad := inner - visWidth
		if pad < 0 {
			pad = 0
		}
		return content + strings.Repeat(" ", pad) + "│"
	}

	blank := row("")

	activeID := m.manager.State.ActiveSessionID
	projectName := m.manager.State.Project

	// Top rule: "───── Agency " + remaining dashes + "╮"
	title := "───── Agency "
	titleLen := len([]rune(title)) // pure ASCII, rune count == byte count
	dashCount := inner - titleLen
	if dashCount < 0 {
		dashCount = 0
	}
	top := title + strings.Repeat("─", dashCount) + "╮"

	// Bottom rule.
	bottom := strings.Repeat("─", inner) + "╯"

	var rows []string
	rows = append(rows, top, blank, row(" Project:"), row("   "+truncate(projectName+"/", inner-3)), blank, row(" Sessions:"))

	if len(m.sessions) == 0 {
		rows = append(rows, row("  (none)"))
	} else {
		for i, sess := range m.sessions {
			name := sess.Name
			if name == "" {
				name = sess.Branch
			}
			indicator := "◯"
			if sess.ID == activeID {
				indicator = "◉"
			}
			// Leading space + indicator + space + name; truncate name to fit.
			// Total prefix visible width: 1 (space) + 1 (indicator) + 1 (space) = 3.
			truncName := truncate(name, inner-3)
			label := " " + indicator + " " + truncName
			if i == m.cursor {
				label = selectedStyle.Render(" " + indicator + " " + truncName)
			}
			rows = append(rows, row(label))
		}
	}

	// Error line if any.
	if m.err != nil {
		rows = append(rows, blank, row(errorStyle.Render(truncate("! "+m.err.Error(), inner))))
	}

	// Fill remaining space above help section.
	// Fixed rows at bottom: blank + " Help:" + hint + bottom border = 4 lines.
	helpLines := 3 // " Help:" + hint + blank before help
	fixedRows := len(rows)
	totalRows := m.height
	if totalRows <= 0 {
		totalRows = 24
	}
	// Reserve: helpLines rows + 1 bottom border row.
	fillCount := totalRows - fixedRows - helpLines - 1
	if fillCount < 0 {
		fillCount = 0
	}
	for i := 0; i < fillCount; i++ {
		rows = append(rows, blank)
	}

	// Help section.
	rows = append(rows, blank, row(" Help:"))

	var hint string
	switch {
	case m.confirming:
		confirmSess := m.confirmID
		for _, sess := range m.sessions {
			if sess.ID == m.confirmID {
				confirmSess = sess.Name
				if confirmSess == "" {
					confirmSess = sess.Branch
				}
				break
			}
		}
		hint = errorStyle.Render(fmt.Sprintf("Del %q? [y/n]", truncate(confirmSess, inner-14)))
	case len(m.sessions) == 0:
		hint = " [n] new session"
	default:
		sess := m.sessions[m.cursor]
		if sess.ID == activeID {
			hint = " [⏎] focus  [n] [d]"
		} else {
			hint = " [⏎] switch  [n] [d]"
		}
	}
	rows = append(rows, row(hint), bottom)

	return strings.Join(rows, "\n")
}

// styledStatus returns a colored string representation of a SessionState.
func styledStatus(s state.SessionState) string {
	switch s {
	case state.StateRunning:
		return runningStyle.Render("running")
	case state.StateFailed:
		return failedStyle.Render("failed")
	case state.StatePaused:
		return pausedStyle.Render("paused")
	case state.StateDone:
		return doneStyle.Render("done")
	default:
		return pendingStyle.Render(string(s))
	}
}

// relativeTime formats a time.Time as a human-readable relative duration.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// friendlyError translates common internal/git errors into user-friendly
// messages. Unknown errors are returned with their raw message stripped of
// noisy git output.
func friendlyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()

	switch {
	case strings.Contains(msg, "already has an active session"):
		return errors.New("that branch already has an active session — choose a different branch name")
	case strings.Contains(msg, "already checked out"):
		return errors.New("that branch already has an active worktree — choose a different branch name")
	case strings.Contains(msg, "already exists"):
		return errors.New("a worktree for that branch already exists — choose a different branch name")
	case strings.Contains(msg, "not a valid branch name"):
		return errors.New("invalid branch name — use only alphanumeric characters, dashes, underscores, and slashes")
	case strings.Contains(msg, "docker is not available"):
		return errors.New("docker is not running — start Docker Desktop and try again")
	case strings.Contains(msg, "docker daemon is not running"):
		return errors.New("docker daemon is not reachable — start Docker Desktop and try again")
	case strings.Contains(msg, "No such image"):
		return fmt.Errorf("sandbox image not found — run 'docker pull' for your configured image first")
	case strings.Contains(msg, "Conflict") && strings.Contains(msg, "name"):
		return errors.New("a container with that name already exists — delete the old session first or choose a different branch")
	default:
		// Strip multi-line git output noise; keep only the first line.
		if idx := strings.Index(msg, "\n"); idx > 0 {
			msg = msg[:idx]
		}
		return errors.New(msg)
	}
}
