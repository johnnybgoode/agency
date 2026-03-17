package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	removingStyle = lipgloss.NewStyle().Strikethrough(true).Foreground(lipgloss.Color("8"))
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

// quitStep tracks the stage of the graceful-quit flow.
type quitStep int

const (
	quitIdle            quitStep = iota
	quitAssessing                // async git-status check in flight
	quitConfirmingQuit           // "Quit? N active workspaces [y/N]"
	quitConfirmingDirty          // per-workspace "Kill <name> with unsaved changes? [y/N]"
)

// quitAssessedMsg is emitted when AssessQuitStatuses completes.
type quitAssessedMsg struct {
	infos []workspace.QuitInfo
	err   error
}

// popupRunner abstracts the tmux operations needed by installAgentsCmd.
// The default implementation delegates to *tmux.Client; tests inject a fake.
type popupRunner interface {
	DisplayPopup(cmd string, width, height, x int) error
	SendRawKeyToPane(paneID, key string) error
}

// --- Model ---

// listModel is the top-level Bubble Tea model for the sidebar workspace list view.
type listModel struct {
	manager           *workspace.Manager
	workspaces        []*state.Workspace
	cursor            int
	width             int
	height            int
	err               error
	confirming        bool // inline delete confirm (shown in help area)
	confirmID         string
	removing          map[string]bool // workspace IDs currently being torn down
	agencyBin         string          // absolute path to the agency binary for popup invocation
	quitStep          quitStep
	quitInfos         []workspace.QuitInfo
	dirtyQueue        []*state.Workspace // ACTIVE+DIRTY workspaces awaiting per-ws confirm
	shouldKillSession bool
	popup             popupRunner                     // defaults to manager.Tmux; override in tests
	installerCmd      func(containerID string) string // defaults to installerCmdFor; override in tests
}

// newListModel constructs the list model, pre-populating the workspace list.
func newListModel(mgr *workspace.Manager) listModel {
	bin := "agency"
	if exe, err := os.Executable(); err == nil {
		bin = exe
	}
	return listModel{
		manager:      mgr,
		workspaces:   mgr.List(),
		removing:     make(map[string]bool),
		agencyBin:    bin,
		popup:        mgr.Tmux,
		installerCmd: installerCmdFor,
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
		slog.Info("deletion confirmed", "workspace", id)
		m.confirming = false
		m.confirmID = ""
		m.removing[id] = true
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

// installerCmdFor returns the shell command string used to run the agent
// installer inside the container identified by containerID.
// Single quotes around the bash -c argument prevent the host shell from
// expanding ~ before the command reaches the container.
func installerCmdFor(containerID string) string {
	return fmt.Sprintf("docker exec -it %s bash -c 'bash ~/subagents/install-agents.sh --install-dir local'", containerID)
}

// installAgentsCmd opens an interactive agent installer popup for the workspace,
// then sends C-d (EOF) to the workspace pane to exit Claude. The trapCmd loop
// restarts Claude with --continue so new agents are immediately available.
// Only valid for RUNNING workspaces.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) installAgentsCmd(ws *state.Workspace) tea.Cmd {
	const popupWidth = 80
	const popupHeight = 30
	popup := m.popup
	installerCmd := m.installerCmd(ws.SandboxID)
	paneID := ws.PaneID
	return func() tea.Msg {
		_ = popup.DisplayPopup(installerCmd, popupWidth, popupHeight, 0)
		if paneID != "" {
			_ = popup.SendRawKeyToPane(paneID, "C-d")
		}
		return tickMsg{}
	}
}

// handleQuitMsg processes messages while a quit flow is in progress.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) handleQuitMsg(msg tea.Msg) (listModel, tea.Cmd) {
	// Always reschedule the tick so polling continues if quit is aborted.
	if _, ok := msg.(tickMsg); ok {
		return m, tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg{} })
	}

	switch m.quitStep {
	case quitAssessing:
		if assessed, ok := msg.(quitAssessedMsg); ok {
			m.quitInfos = assessed.infos
			activeCount := 0
			for _, info := range m.quitInfos {
				if info.IsActive {
					activeCount++
				}
			}
			if activeCount == 0 {
				return m.startExecuting()
			}
			m.quitStep = quitConfirmingQuit
		}

	case quitConfirmingQuit:
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "y":
				var dirtyQueue []*state.Workspace
				for _, info := range m.quitInfos {
					if info.IsActive && info.IsDirty {
						dirtyQueue = append(dirtyQueue, info.WS)
					}
				}
				if len(dirtyQueue) > 0 {
					m.dirtyQueue = dirtyQueue
					m.quitStep = quitConfirmingDirty
					return m, nil
				}
				return m.startExecuting()
			case "n", "esc":
				_ = m.manager.Tmux.DetachClients()
				m.quitStep = quitIdle
			}
		}

	case quitConfirmingDirty:
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "y":
				m.dirtyQueue = m.dirtyQueue[1:]
				if len(m.dirtyQueue) > 0 {
					return m, nil
				}
				return m.startExecuting()
			case "n", "esc":
				_ = m.manager.Tmux.DetachClients()
				m.quitStep = quitIdle
				m.dirtyQueue = nil
			}
		}

	}

	return m, nil
}

// startExecuting signals a graceful quit: sets shouldKillSession so runSidebar
// can do cleanup after p.Run() returns, then exits the TUI immediately.
// Container stops are fired as non-blocking background calls in runSidebar so
// the user gets their shell prompt back without waiting for docker.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) startExecuting() (listModel, tea.Cmd) {
	m.shouldKillSession = true
	return m, tea.Quit
}

// buildQuitModal constructs the DangerModal for the current quit confirmation step.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) buildQuitModal() DangerModal {
	if m.quitStep == quitConfirmingQuit {
		activeCount := 0
		for _, info := range m.quitInfos {
			if info.IsActive {
				activeCount++
			}
		}
		return DangerModal{
			Title:  "Quit Agency?",
			Lines:  []string{fmt.Sprintf("%d active workspace(s)", activeCount), "will be paused."},
			Prompt: "[y] yes   [N] cancel",
		}
	}
	// quitConfirmingDirty
	name := ""
	if len(m.dirtyQueue) > 0 {
		name = m.dirtyQueue[0].Name
		if name == "" {
			name = m.dirtyQueue[0].Branch
		}
	}
	return DangerModal{
		Title:  "Unsaved changes",
		Lines:  []string{"Pause " + truncate(name, 14) + "?", "Changes will be kept."},
		Prompt: "[y] yes   [N] cancel",
	}
}

// handleNormalKey handles key presses in normal (non-confirming) mode.
//
//nolint:gocritic,gocyclo // bubbletea model must use value receivers; key dispatch is inherently branchy
func (m listModel) handleNormalKey(msg tea.KeyMsg) (listModel, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		slog.Info("quit requested")
		m.quitStep = quitAssessing
		mgr := m.manager
		return m, func() tea.Msg {
			infos, err := mgr.AssessQuitStatuses(context.Background())
			return quitAssessedMsg{infos: infos, err: err}
		}

	case "n":
		slog.Info("new workspace popup requested")
		return m, m.newWorkspaceCmd()

	case "enter":
		if len(m.workspaces) > 0 && m.cursor < len(m.workspaces) {
			ws := m.workspaces[m.cursor]
			slog.Info("workspace selected", "workspace", ws.ID)
			activeID := m.manager.State.ActiveWorkspaceID
			mainWindowID := m.manager.State.MainWindowID
			if ws.PaneID != "" && mainWindowID != "" {
				if activeID == ws.ID {
					// Already active — focus the pane by selecting the main window.
					_ = m.manager.Tmux.SelectWindow(mainWindowID)
				} else {
					_ = m.manager.SwapActivePane(ws.ID)
					_ = m.manager.Tmux.SelectWindow(mainWindowID)
					applyStatusBar(m.manager)
				}
			} else if ws.TmuxWindow != "" {
				// Fallback: just select the window.
				_ = m.manager.Tmux.SelectWindow(ws.TmuxWindow)
			}
		}

	case "s":
		if len(m.workspaces) > 0 && m.cursor < len(m.workspaces) {
			ws := m.workspaces[m.cursor]
			if ws.State == state.StateRunning && ws.SandboxID != "" {
				return m, m.installAgentsCmd(ws)
			}
		}

	case "d":
		if len(m.workspaces) > 0 && m.cursor < len(m.workspaces) {
			slog.Info("deletion requested", "workspace", m.workspaces[m.cursor].ID)
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
//nolint:gocritic,gocyclo // bubbletea model must use value receivers; message dispatch is inherently branchy
func (m listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle quit state machine first (suppresses all other input while active).
	if m.quitStep != quitIdle {
		return m.handleQuitMsg(msg)
	}

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
		// Skip all state access during async removal to avoid data races
		// with the Remove goroutine modifying state concurrently.
		if len(m.removing) == 0 {
			// Reload workspaces from state file on each tick.
			if s, err := state.Read(m.manager.StatePath); err == nil {
				m.manager.State = s
				m.workspaces = m.manager.List()
				if m.cursor >= len(m.workspaces) && len(m.workspaces) > 0 {
					m.cursor = len(m.workspaces) - 1
				}
			}
			// Detect dead/displaced panes and clear stale state.
			verifyLayoutIntegrity(m.manager)
			// Create the right-pane split when the first workspace appears.
			ensureSplitOnFirstWorkspace(m.manager)
			// Update status bar with current workspace info.
			applyStatusBar(m.manager)
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
			slog.Error("workspace creation failed", "error", msg.err)
			m.err = friendlyError(msg.err)
		} else {
			m.err = nil
		}
		if m.cursor >= len(m.workspaces) && len(m.workspaces) > 0 {
			m.cursor = len(m.workspaces) - 1
		}

	case workspaceRemovedMsg:
		delete(m.removing, msg.id)
		// Force reload from disk to get the authoritative post-removal state.
		if s, err := state.Read(m.manager.StatePath); err == nil {
			m.manager.State = s
		}
		m.workspaces = m.manager.List()
		if msg.err != nil {
			slog.Error("workspace removal failed", "workspace", msg.id, "error", msg.err)
			m.err = friendlyError(msg.err)
		} else {
			m.err = nil
		}
		if m.cursor >= len(m.workspaces) && len(m.workspaces) > 0 {
			m.cursor = len(m.workspaces) - 1
		}
		// Collapse right pane when all workspaces are gone (return to zero state).
		verifyLayoutIntegrity(m.manager)
		applyStatusBar(m.manager)

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

// sidebarWidth returns the sidebar total width in columns for rendering.
// In zero state (no workspaces), the sidebar is drawn within the full terminal
// at a percentage-based width clamped between 25 and 50 columns.
// In sidebar mode (workspaces exist), the sidebar fills its tmux pane (m.width),
// enforcing only the minimum of 25 columns.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) sidebarWidth() int {
	const maxZeroState = 50
	min := workspace.MinSidebarColumns

	if len(m.workspaces) == 0 {
		// Zero state: percentage of full terminal, clamped [25, 50].
		cols := m.manager.SidebarColumns(m.width)
		if cols > maxZeroState {
			cols = maxZeroState
		}
		return cols
	}

	// Sidebar mode: fill the pane width.
	w := m.width
	if w < min {
		w = min
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
		return string(runes[:maxLen])
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

// workspaceLabel returns the styled label string for a single workspace row.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) workspaceLabel(ws *state.Workspace, idx int, activeID string, inner int) string {
	name := ws.Name
	if name == "" {
		name = ws.Branch
	}
	// Leading space + indicator + space + name; truncate name to fit.
	// Total prefix visible width: 1 (space) + 1 (indicator) + 1 (space) = 3.
	truncName := truncate(name, inner-3)
	if m.removing[ws.ID] {
		return removingStyle.Render(" ✗ " + truncName)
	}
	indicator := "◯"
	if ws.ID == activeID {
		indicator = "◉"
	}
	if idx == m.cursor {
		return selectedStyle.Render(" " + indicator + " " + truncName)
	}
	return " " + indicator + " " + truncName
}

//nolint:gocritic,gocyclo // bubbletea model must use value receivers; sidebar rendering branches on many state combinations
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

	totalRows := m.height
	if totalRows <= 0 {
		totalRows = 24
	}

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

	// Modal overlay: replace workspace list + help section with a danger dialog.
	if m.quitStep == quitConfirmingQuit || m.quitStep == quitConfirmingDirty {
		modal := m.buildQuitModal()
		modalRows := modal.Rows(inner)

		available := totalRows - len(rows) - 1 // -1 for bottom border
		if available < 0 {
			available = 0
		}
		topPad := (available - len(modalRows)) / 2
		if topPad < 0 {
			topPad = 0
		}
		for i := 0; i < topPad; i++ {
			rows = append(rows, blank)
		}
		for _, r := range modalRows {
			rows = append(rows, r+"│")
		}
		bottomPad := available - topPad - len(modalRows)
		if bottomPad < 0 {
			bottomPad = 0
		}
		for i := 0; i < bottomPad; i++ {
			rows = append(rows, blank)
		}
		rows = append(rows, bottom)
		return strings.Join(rows, "\n")
	}

	if len(m.workspaces) == 0 {
		rows = append(rows, row("  (none)"))
	} else {
		for i, ws := range m.workspaces {
			rows = append(rows, row(m.workspaceLabel(ws, i, activeID, inner)))
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
	case m.quitStep == quitAssessing:
		hint = helpStyle.Render(" checking workspaces...")
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
			hint = " [⏎] focus  [n] [d] [s]"
		} else {
			hint = " [⏎] switch  [n] [d] [s]"
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
		return errors.New("sandbox image not found — run 'docker pull' for your configured image first")
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
