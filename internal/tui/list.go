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
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("8"))
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

// sessionCreatedMsg is emitted after an async Create call completes.
type sessionCreatedMsg struct {
	sess *state.Session
	err  error
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

// listModel is the top-level Bubble Tea model for the session list view.
type listModel struct {
	manager        *session.Manager
	sessions       []*state.Session
	cursor         int
	width          int
	height         int
	err            error
	creating       bool
	createForm     createModel
	confirming     bool
	confirmID      string
	selectedWindow string // set when user presses Enter; triggers tmux attach after quit
}

// newListModel constructs the list model, pre-populating the session list.
func newListModel(mgr *session.Manager) listModel {
	return listModel{
		manager:  mgr,
		sessions: mgr.List(),
	}
}

// Init returns no initial command.
func (m listModel) Init() tea.Cmd {
	return nil
}

// Update handles all incoming messages and key presses.
func (m listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Delegate to create form when it is active.
	if m.creating {
		var cmd tea.Cmd
		m.createForm, cmd = m.createForm.Update(msg)

		if m.createForm.submitted {
			branch := m.createForm.input.Value()
			m.creating = false
			mgr := m.manager
			return m, func() tea.Msg {
				sess, err := mgr.Create(context.Background(), branch)
				return sessionCreatedMsg{sess: sess, err: err}
			}
		}
		if m.createForm.cancelled {
			m.creating = false
			return m, nil
		}
		return m, cmd
	}

	// Handle delete confirmation dialog.
	if m.confirming {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
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
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit

		case "n":
			m.creating = true
			m.createForm = newCreateModel(m.manager.Cfg.Worktree.BranchPrefix)
			return m, m.createForm.Init()

		case "enter":
			if len(m.sessions) > 0 && m.cursor < len(m.sessions) {
				sess := m.sessions[m.cursor]
				if sess.TmuxWindow != "" {
					_ = m.manager.Tmux.SelectWindow(sess.TmuxWindow)
					m.selectedWindow = sess.TmuxWindow
					return m, tea.Quit
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

	case sessionCreatedMsg:
		m.sessions = m.manager.List()
		m.creating = false
		if msg.err != nil {
			m.err = friendlyError(msg.err)
		} else {
			m.err = nil
		}
		// Keep cursor in bounds.
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

// View renders the full TUI screen.
func (m listModel) View() string {
	if m.creating {
		return m.createForm.View()
	}

	if m.confirming {
		return "\n  " + errorStyle.Render(fmt.Sprintf("Delete session %s? (y/n)", m.confirmID)) + "\n"
	}

	var b strings.Builder

	// Title
	b.WriteString("\n  " + titleStyle.Render("agency — session manager") + "\n\n")

	// Column headers
	b.WriteString("  " + headerStyle.Render(fmt.Sprintf("%-16s  %-26s  %-12s  %s", "ID", "BRANCH", "STATUS", "CREATED")) + "\n")
	b.WriteString("  " + headerStyle.Render(strings.Repeat("─", 68)) + "\n")

	if len(m.sessions) == 0 {
		b.WriteString("\n  No sessions. Press 'n' to create one.\n")
	} else {
		for i, sess := range m.sessions {
			cursor := "  "
			var idStr string
			if i == m.cursor {
				cursor = "> "
				idStr = selectedStyle.Render(sess.ID)
			} else {
				idStr = sess.ID
			}

			branch := sess.Branch
			if len(branch) > 24 {
				branch = branch[:24]
			}

			statusStr := styledStatus(sess.State)
			relTime := relativeTime(sess.CreatedAt)

			line := fmt.Sprintf("%s%-16s  %-26s  %-12s  %s",
				cursor,
				idStr,
				branch,
				statusStr,
				relTime,
			)
			b.WriteString("  " + line + "\n")
		}
	}

	b.WriteString("\n")

	// Error line.
	if m.err != nil {
		b.WriteString("  " + errorStyle.Render("error: "+m.err.Error()) + "\n\n")
	}

	// Help footer.
	b.WriteString("  " + helpStyle.Render("n: new  enter: switch  d: delete  r: refresh  q: quit") + "\n")

	return b.String()
}

// styledStatus returns a coloured string representation of a SessionState.
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
