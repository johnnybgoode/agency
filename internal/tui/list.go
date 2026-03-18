package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/johnnybgoode/agency/internal/sandbox"
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

// syncDoneMsg is emitted after an async SyncHome call completes.
type syncDoneMsg struct {
	workspaceName string
	result        *workspace.SyncResult
	err           error
}

// quitStep tracks the stage of the graceful-quit flow.
type quitStep int

const (
	quitIdle            quitStep = iota
	quitConfirmingQuit           // "Quit? N active workspaces [y/N]"
	quitConfirmingDirty          // per-workspace "Kill <name> with unsaved changes? [y/N]"
)

// quitPopupDoneMsg is emitted when the quit popup closes.
type quitPopupDoneMsg struct {
	confirmed bool
	infos     []workspace.QuitInfo
}

// popupRunner abstracts the tmux operations needed by installAgentsCmd.
// The default implementation delegates to *tmux.Client; tests inject a fake.
type popupRunner interface {
	CapturePane(paneID string) (string, error)
	DisplayPopup(cmd string, width, height, x int) error
	SendKeysToPane(paneID, keys string) error
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
	removing          map[string]bool      // workspace IDs currently being torn down
	agencyBin         string               // absolute path to the agency binary for popup invocation
	quitInfos         []workspace.QuitInfo // populated by popup result for quit cleanup
	shouldKillSession bool
	lastActiveID      string                          // tracks active workspace ID to detect changes
	popup             popupRunner                     // defaults to manager.Tmux; override in tests
	installerCmd      func(containerID string) string // defaults to installerCmdFor; override in tests
	sleepFn           func(time.Duration)             // defaults to time.Sleep; override in tests
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
		lastActiveID: mgr.State.ActiveWorkspaceID,
		agencyBin:    bin,
		popup:        mgr.Tmux,
		installerCmd: installerCmdFor,
		sleepFn:      time.Sleep,
	}
}

// syncCursorToActive moves the cursor to the index of the active workspace.
// If no workspace is active or the active ID is not in the list, the cursor
// is left unchanged (but still clamped to bounds).
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) syncCursorToActive() listModel {
	activeID := m.manager.State.ActiveWorkspaceID
	if activeID == "" {
		return m
	}
	for i, ws := range m.workspaces {
		if ws.ID == activeID {
			m.cursor = i
			return m
		}
	}
	return m
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

// isAtPrompt reports whether the pane content looks like Claude Code is sitting
// at its idle input prompt — typically just a ">" on the last non-empty line.
// This is used to decide whether Escape keys are needed to dismiss a sub-command
// dialog before injecting text. Sending unnecessary Escapes at the idle prompt
// would trigger Claude's /rewind shortcut (double-tap Esc).
//
// The detection is intentionally conservative: any non-trivial content on the
// last line means "not at prompt", so we err on the side of sending Escape
// rather than accidentally skipping it when a dialog is open.
func isAtPrompt(paneContent string) bool {
	lines := strings.Split(paneContent, "\n")
	// Walk backwards to find the last non-empty line.
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], " \t")
		if line == "" {
			continue
		}
		// Claude Code's idle prompt is a single ">" possibly followed by
		// whitespace (the cursor position). Some terminals append a space
		// or two after the ">".
		return line == ">" || line == "> "
	}
	// Completely empty pane — treat as idle.
	return true
}

// clearPaneInput uses tmux capture-pane to check whether the pane shows Claude's
// idle prompt. If not idle it sends a single Escape, waits for the terminal to
// process it, and rechecks — up to maxEsc attempts. This avoids sending
// unnecessary Escapes that would trigger Claude's /rewind shortcut.
//
// This function is intentionally generalized: any future code that needs to
// restore an agent to its prompt before injecting keystrokes can call it.
func clearPaneInput(popup popupRunner, sleepFn func(time.Duration), paneID string) {
	const maxEsc = 3
	for range maxEsc {
		content, err := popup.CapturePane(paneID)
		if err == nil && isAtPrompt(content) {
			return // already at prompt, no Escape needed
		}
		sleepFn(50 * time.Millisecond)
		_ = popup.SendRawKeyToPane(paneID, "Escape")
	}
	// Final wait for the last Escape to be processed.
	sleepFn(100 * time.Millisecond)
}

// agentFiles returns the set of .md filenames currently present in dir.
// Returns an empty map if the directory does not exist or cannot be read.
func agentFiles(dir string) map[string]struct{} {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string]struct{}{}
	}
	files := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files[e.Name()] = struct{}{}
		}
	}
	return files
}

// hasNewAgents reports whether dir contains any .md files not present in before.
func hasNewAgents(dir string, before map[string]struct{}) bool {
	for name := range agentFiles(dir) {
		if _, seen := before[name]; !seen {
			return true
		}
	}
	return false
}

// installAgentsCmd opens an interactive agent installer popup for the workspace.
// After the popup closes it checks whether any new agent .md files were added to
// the workspace's .claude/agents directory. Only if new agents were installed does
// it send C-d to the workspace pane so the trapCmd loop restarts Claude with
// --continue (making the new agents available).
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) installAgentsCmd(ws *state.Workspace) tea.Cmd {
	const popupWidth = 80
	const popupHeight = 30
	popup := m.popup
	sleepFn := m.sleepFn
	if err := sandbox.ValidateContainerID(ws.SandboxID); err != nil {
		slog.Error("refusing to run installer: invalid container ID", "sandbox_id", ws.SandboxID, "error", err)
		return func() tea.Msg {
			return reconcileDoneMsg{err: fmt.Errorf("invalid container ID: %w", err)}
		}
	}
	installerCmd := m.installerCmd(ws.SandboxID)
	paneID := ws.PaneID
	agentsDir := filepath.Join(ws.WorktreePath, ".claude", "agents")
	return func() tea.Msg {
		before := agentFiles(agentsDir)
		_ = popup.DisplayPopup(installerCmd, popupWidth, popupHeight, 0)
		if paneID != "" && hasNewAgents(agentsDir, before) {
			// Clear any open command dialog before injecting /reload-plugins.
			// We use tmux capture-pane to detect whether the Claude session is
			// already at its idle prompt; if it is, no Escape keys are needed.
			// Sending unnecessary Escapes would trigger Claude's /rewind
			// shortcut (double-tap Esc). We try up to 3 Escape rounds,
			// checking the pane each time, and stop as soon as the prompt
			// appears idle.
			clearPaneInput(popup, sleepFn, paneID)
			_ = popup.SendKeysToPane(paneID, "/reload-plugins")
			_ = popup.SendRawKeyToPane(paneID, "C-d")
		}
		return tickMsg{}
	}
}

// quitPopupCmd returns a tea.Cmd that launches the quit confirmation popup.
// It runs the quit popup in a tmux display-popup, then reads the result file.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) quitPopupCmd() tea.Cmd {
	const popupWidth = 50
	const popupHeight = 12
	tmuxClient := m.manager.Tmux
	agencyBin := m.agencyBin
	mgr := m.manager
	return func() tea.Msg {
		_ = tmuxClient.DisplayPopup(agencyBin+" quit --popup", popupWidth, popupHeight, 0)

		// Read the result file written by the popup process.
		resultPath := filepath.Join(mgr.ProjectDir, ".agency", QuitResultFile)
		data, err := os.ReadFile(resultPath)
		if err != nil {
			slog.Warn("quit popup result not found", "error", err)
			return quitPopupDoneMsg{confirmed: false}
		}
		_ = os.Remove(resultPath) // Clean up.

		var result QuitResultData
		if err := json.Unmarshal(data, &result); err != nil {
			slog.Warn("quit popup result parse error", "error", err)
			return quitPopupDoneMsg{confirmed: false}
		}

		if !result.Confirmed {
			return quitPopupDoneMsg{confirmed: false}
		}

		// Re-assess to get fresh QuitInfo for cleanup.
		infos, err := mgr.AssessQuitStatuses(context.Background())
		if err != nil {
			slog.Error("quit re-assess failed", "error", err)
			return quitPopupDoneMsg{confirmed: false}
		}

		return quitPopupDoneMsg{confirmed: true, infos: infos}
	}
}

// confirmQuit signals a graceful quit: sets shouldKillSession so runSidebar
// can do cleanup after p.Run() returns, then exits the TUI immediately.
// Container stops are fired as non-blocking background calls in runSidebar so
// the user gets their shell prompt back without waiting for docker.
//
//nolint:gocritic // bubbletea model must use value receivers
func (m listModel) confirmQuit() (listModel, tea.Cmd) {
	m.shouldKillSession = true
	return m, tea.Quit
}

// handleNormalKey handles key presses in normal (non-confirming) mode.
//
//nolint:gocritic,gocyclo // bubbletea model must use value receivers; key dispatch is inherently branchy
func (m listModel) handleNormalKey(msg tea.KeyMsg) (listModel, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		slog.Info("quit requested via popup")
		return m, m.quitPopupCmd()

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

	case "S":
		if len(m.workspaces) > 0 && m.cursor < len(m.workspaces) {
			ws := m.workspaces[m.cursor]
			if ws.SandboxID != "" {
				mgr := m.manager
				wsName := ws.DisplayName()
				wsID := ws.ID
				return m, func() tea.Msg {
					result, err := mgr.SyncHome(context.Background(), wsID, workspace.SyncOpts{})
					return syncDoneMsg{workspaceName: wsName, result: result, err: err}
				}
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
			// Re-read state from disk. The sidebar holds the project flock (acquired in
			// runSidebar), so concurrent writes from popup processes serialize correctly.
			if s, err := state.Read(m.manager.StatePath); err == nil {
				m.manager.State = s
				m.workspaces = m.manager.List()
				if m.manager.State.ActiveWorkspaceID != m.lastActiveID {
					m = m.syncCursorToActive()
					m.lastActiveID = m.manager.State.ActiveWorkspaceID
				}
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
		m = m.syncCursorToActive()
		m.lastActiveID = m.manager.State.ActiveWorkspaceID
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
		m = m.syncCursorToActive()
		m.lastActiveID = m.manager.State.ActiveWorkspaceID
		if m.cursor >= len(m.workspaces) && len(m.workspaces) > 0 {
			m.cursor = len(m.workspaces) - 1
		}
		// Collapse right pane when all workspaces are gone (return to zero state).
		verifyLayoutIntegrity(m.manager)
		applyStatusBar(m.manager)

	case quitPopupDoneMsg:
		if msg.confirmed {
			m.quitInfos = msg.infos
			return m.confirmQuit()
		}
		// Popup was canceled — resume normal operation.

	case reconcileDoneMsg:
		m.workspaces = m.manager.List()
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
		}

	case syncDoneMsg:
		if msg.err != nil {
			m.err = friendlyError(msg.err)
		} else {
			synced := len(msg.result.Copied)
			skipped := len(msg.result.Skipped)
			if skipped > 0 {
				m.err = fmt.Errorf("synced %d file(s) from %s, %d skipped (host is newer)", synced, msg.workspaceName, skipped)
			} else {
				m.err = fmt.Errorf("synced %d file(s) from %s", synced, msg.workspaceName)
			}
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
	name := ws.DisplayName()
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

//nolint:gocritic // bubbletea model must use value receivers; sidebar rendering branches on many state combinations
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
	case m.confirming:
		confirmWS := m.confirmID
		for _, ws := range m.workspaces {
			if ws.ID == m.confirmID {
				confirmWS = ws.DisplayName()
				break
			}
		}
		hint = errorStyle.Render(fmt.Sprintf(" del %s [y/n]", truncate(confirmWS, inner-11)))
	case len(m.workspaces) == 0:
		hint = " [n] new workspace"
	default:
		ws := m.workspaces[m.cursor]
		if ws.ID == activeID {
			hint = " [⏎] focus  [n] [d] [s] [S]sync"
		} else {
			hint = " [⏎] switch  [n] [d] [s] [S]sync"
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
