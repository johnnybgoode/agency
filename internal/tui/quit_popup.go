package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/workspace"
)

// quitPopupModel is a standalone bubbletea model for the quit confirmation popup.
type quitPopupModel struct {
	infos       []workspace.QuitInfo
	theme       config.ThemeConfig
	step        quitStep
	dirtyQueue  []*state.Workspace
	result      QuitResultData
	width       int
	height      int
	activeCount int // number of active workspaces, computed once in constructor
}

func newQuitPopupModel(infos []workspace.QuitInfo, theme config.ThemeConfig) quitPopupModel {
	m := quitPopupModel{
		infos: infos,
		theme: theme,
	}
	for _, info := range infos {
		if info.IsActive {
			m.activeCount++
		}
	}
	if m.activeCount == 0 {
		// No active workspaces — auto-confirm.
		m.result = QuitResultData{Confirmed: true, Infos: infos}
		m.step = quitIdle // will quit immediately
	} else {
		m.step = quitConfirmingQuit
	}

	return m
}

//nolint:gocritic // bubbletea model must use value receivers
func (m quitPopupModel) Init() tea.Cmd {
	if m.result.Confirmed {
		return tea.Quit
	}
	return nil
}

//nolint:gocritic // bubbletea model must use value receivers
func (m quitPopupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch m.step {
		case quitConfirmingQuit:
			switch msg.String() {
			case "y":
				var dirtyQueue []*state.Workspace
				for _, info := range m.infos {
					if info.IsActive && info.IsDirty {
						dirtyQueue = append(dirtyQueue, info.WS)
					}
				}
				if len(dirtyQueue) > 0 {
					m.dirtyQueue = dirtyQueue
					m.step = quitConfirmingDirty
					return m, nil
				}
				m.result = QuitResultData{Confirmed: true, Infos: m.infos}
				return m, tea.Quit
			case "n", "esc", "q", "ctrl+c":
				m.result = QuitResultData{Confirmed: false}
				return m, tea.Quit
			}

		case quitConfirmingDirty:
			switch msg.String() {
			case "y":
				m.dirtyQueue = m.dirtyQueue[1:]
				if len(m.dirtyQueue) > 0 {
					return m, nil
				}
				m.result = QuitResultData{Confirmed: true, Infos: m.infos}
				return m, tea.Quit
			case "n", "esc", "q", "ctrl+c":
				m.result = QuitResultData{Confirmed: false}
				return m, tea.Quit
			}
		}
	}

	return m, nil
}

//nolint:gocritic // bubbletea model must use value receivers
func (m quitPopupModel) View() string {
	modal := m.buildModal()
	w := m.width
	if w <= 0 {
		w = 50
	}
	h := m.height
	if h <= 0 {
		h = 12
	}

	normal, _ := modal.styles()

	rows := modal.Rows(w)

	// Pad to fill the popup height.
	for len(rows) < h {
		rows = append(rows, normal.Render(strings.Repeat(" ", w)))
	}

	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

//nolint:gocritic // bubbletea model must use value receivers
func (m quitPopupModel) buildModal() DangerModal {
	bg := m.theme.DangerBg
	fg := m.theme.DangerFg

	if m.step == quitConfirmingQuit {
		return DangerModal{
			Title:  "Quit Agency?",
			Lines:  []string{fmt.Sprintf("%d active workspace(s)", m.activeCount), "will be paused."},
			Prompt: "[y] yes   [n] cancel",
			Bg:     bg,
			Fg:     fg,
		}
	}

	// quitConfirmingDirty
	name := ""
	if len(m.dirtyQueue) > 0 {
		name = m.dirtyQueue[0].DisplayName()
	}
	return DangerModal{
		Title:  "Unsaved changes",
		Lines:  []string{"Pause " + truncate(name, 20) + "?", "Changes will be kept."},
		Prompt: "[y] yes   [n] cancel",
		Bg:     bg,
		Fg:     fg,
	}
}
