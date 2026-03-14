package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/workspace"
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

// tickMsg is emitted on the polling interval to reload workspace state.
type tickMsg struct{}

// workspaceCreatedMsg is emitted after an async Create call completes.
type workspaceCreatedMsg struct {
	err error
}

// workspaceRemovedMsg is emitted after an async Remove call completes.
type workspaceRemovedMsg struct {
	id  string
	err error
}

// reconcileDoneMsg is emitted after an async Reconcile call completes.
type reconcileDoneMsg struct {
	err error
}

// --- Model ---

// listModel is the top-level Bubble Tea model for the sidebar workspace list view.
type listModel struct {
	manager    *workspace.Manager
	workspaces []*state.Workspace
	cursor     int
	width      int
	height     int
	err        error
	confirming bool // inline delete confirm (shown in help area)
	confirmID  string
	agencyBin  string // absolute path to the agency binary for popup invocation
}

// newListModel constructs the list model, pre-populating the workspace list.
func newListModel(mgr *workspace.Manager) listModel {
	bin := "agency"
	if exe, err := os.Executable(); err == nil {
		bin = exe
	}
	return listModel{
		manager:    mgr,
		workspaces: mgr.List(),
		agencyBin:  bin,
	}
}

// Init returns the initial commands: an immediate background reconcile and the
// first polling tick.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) Init() tea.Cmd {
	mgr := m.manager
	return tea.Batch(
		func() tea.Msg {
			err := mgr.Reconcile(context.Background())
			return reconcileDoneMsg{err: err}
		},
		tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
			return tickMsg{}
		}),
	)
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
			// If removing the active workspace, switch to the last active one
			// immediately (fast tmux op) before the slow teardown begins. This
			// also breaks the pane out of the main window, so Remove can safely
			// kill its now-detached window without touching the sidebar.
			if mgr.State.ActiveWorkspaceID == id {
				mgr.SwitchToLastActive()
				_ = mgr.SaveState()
			}
			err := mgr.Remove(context.Background(), id)
			return workspaceRemovedMsg{id: id, err: err}
		}
	case "n", "esc":
		m.confirming = false
		m.confirmID = ""
	}
	return m, nil
}

// newWorkspaceCmd returns a tea.Cmd that opens the new-workspace popup.
// In zero state the popup is centered over the right welcome panel.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) newWorkspaceCmd() tea.Cmd {
	const popupWidth = 60
	const popupHeight = 10
	xPos := 0
	if len(m.workspaces) == 0 && m.width > m.sidebarWidth() {
		rightStart := m.sidebarWidth()
		rightWidth := m.width - rightStart
		xPos = rightStart + (rightWidth-popupWidth)/2
		if xPos < rightStart {
			xPos = rightStart
		}
	}
	tmuxClient := m.manager.Tmux
	agencyBin := m.agencyBin
	return func() tea.Msg {
		_ = tmuxClient.DisplayPopup(agencyBin+" new --popup", popupWidth, popupHeight, xPos)
		return tickMsg{} // refresh workspace list after the popup closes
	}
}

// handleNormalKey handles key presses in normal (non-confirming) mode.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) handleNormalKey(msg tea.KeyMsg) (listModel, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "n":
		return m, m.newWorkspaceCmd()

	case "enter":
		if len(m.workspaces) > 0 && m.cursor < len(m.workspaces) {
			ws := m.workspaces[m.cursor]
			activeID := m.manager.State.ActiveWorkspaceID
			mainWindowID := m.manager.State.MainWindowID
			if ws.PaneID != "" && mainWindowID != "" {
				if activeID == ws.ID {
					// Already active — focus the pane by selecting the main window.
					_ = m.manager.Tmux.SelectWindow(mainWindowID)
				} else {
					m.manager.SwitchActivePane(ws)
					_ = m.manager.SaveState()
					_ = m.manager.Tmux.SelectWindow(mainWindowID)
				}
			} else if ws.TmuxWindow != "" {
				// Fallback: just select the window.
				_ = m.manager.Tmux.SelectWindow(ws.TmuxWindow)
			}
		}

	case "d":
		if len(m.workspaces) > 0 && m.cursor < len(m.workspaces) {
			m.confirming = true
			m.confirmID = m.workspaces[m.cursor].ID
		}

	case "r":
		mgr := m.manager
		return m, func() tea.Msg {
			err := mgr.Reconcile(context.Background())
			return reconcileDoneMsg{err: err}
		}

	case "j", "down":
		if m.cursor < len(m.workspaces)-1 {
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
		// Reload workspaces from state file on each tick.
		if s, err := state.Read(m.manager.StatePath); err == nil {
			m.manager.State = s
			m.workspaces = m.manager.List()
			if m.cursor >= len(m.workspaces) && len(m.workspaces) > 0 {
				m.cursor = len(m.workspaces) - 1
			}
		}
		return m, tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg{} })

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		return m.handleNormalKey(msg)

	case workspaceCreatedMsg:
		m.workspaces = m.manager.List()
		if msg.err != nil {
			m.err = friendlyError(msg.err)
		} else {
			m.err = nil
		}
		if m.cursor >= len(m.workspaces) && len(m.workspaces) > 0 {
			m.cursor = len(m.workspaces) - 1
		}

	case workspaceRemovedMsg:
		m.workspaces = m.manager.List()
		if msg.err != nil {
			m.err = friendlyError(msg.err)
		} else {
			m.err = nil
		}
		if m.cursor >= len(m.workspaces) && len(m.workspaces) > 0 {
			m.cursor = len(m.workspaces) - 1
		}

	case reconcileDoneMsg:
		m.workspaces = m.manager.List()
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
		}
	}

	return m, nil
}

// sidebarWidth returns the configured sidebar total width (including the right │).
// Defaults to 22 if not set in config.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) sidebarWidth() int {
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
	sidebar := m.renderSidebar()
	if len(m.workspaces) == 0 && m.width > m.sidebarWidth() {
		right := m.renderZeroPanel(m.width-m.sidebarWidth(), m.height)
		return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, right)
	}
	return sidebar
}

//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) renderSidebar() string {
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

	activeID := m.manager.State.ActiveWorkspaceID
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
	rows = append(rows, top, blank, row(" Project:"), row("   "+truncate(projectName+"/", inner-3)), blank, row(" Workspaces:"))

	if len(m.workspaces) == 0 {
		rows = append(rows, row("  (none)"))
	} else {
		for i, ws := range m.workspaces {
			name := ws.Name
			if name == "" {
				name = ws.Branch
			}
			indicator := "◯"
			if ws.ID == activeID {
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
		confirmWS := m.confirmID
		for _, ws := range m.workspaces {
			if ws.ID == m.confirmID {
				confirmWS = ws.Name
				if confirmWS == "" {
					confirmWS = ws.Branch
				}
				break
			}
		}
		hint = errorStyle.Render(fmt.Sprintf(" del %s [y/n]", truncate(confirmWS, inner-11)))
	case len(m.workspaces) == 0:
		hint = " [n] new workspace"
	default:
		ws := m.workspaces[m.cursor]
		if ws.ID == activeID {
			hint = " [⏎] focus  [n] [d]"
		} else {
			hint = " [⏎] switch  [n] [d]"
		}
	}
	rows = append(rows, row(hint), bottom)

	return strings.Join(rows, "\n")
}

//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) renderZeroPanel(width, height int) string {
	titleStr := "Agency"
	divStr := strings.Repeat("─", len(titleStr))
	promptStr := "Create [n]ew workspace..."

	content := lipgloss.JoinVertical(lipgloss.Center,
		lipgloss.NewStyle().Bold(true).Render(titleStr),
		divStr,
		"",
		promptStr,
	)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(2, 6).
		Render(content)

	// Center box in available area; reserve last line for a separator.
	centered := lipgloss.Place(width, height-1, lipgloss.Center, lipgloss.Center, box)
	return centered + "\n" + strings.Repeat("─", width)
}

// styledStatus returns a colored string representation of a WorkspaceState.
func styledStatus(s state.WorkspaceState) string {
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
	case strings.Contains(msg, "already has an active workspace"):
		return errors.New("that branch already has an active workspace — choose a different branch name")
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
		return errors.New("a container with that name already exists — delete the old workspace first or choose a different branch")
	default:
		// Strip multi-line git output noise; keep only the first line.
		if idx := strings.Index(msg, "\n"); idx > 0 {
			msg = msg[:idx]
		}
		return errors.New(msg)
	}
}
