// Package tui implements the terminal user interface using bubbletea.
package tui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// createModel is the Bubble Tea model for the "new session" form.
type createModel struct {
	input     textinput.Model
	submitted bool
	canceled  bool
}

// newCreateModel initializes a create form with the given branch prefix
// pre-filled in the input field.
func newCreateModel(branchPrefix string) createModel {
	ti := textinput.New()
	ti.Placeholder = "branch-name"
	ti.SetValue(branchPrefix)
	// Position the cursor at the end of the pre-filled value.
	ti.CursorEnd()
	ti.Focus()

	return createModel{input: ti}
}

// Init returns the blink command so the text-input cursor animates.
func (m createModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles keyboard events for the create form.
func (m createModel) Update(msg tea.Msg) (createModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			m.submitted = true
			return m, nil
		case "esc":
			m.canceled = true
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View renders the create form.
func (m createModel) View() string {
	formTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render("New Session")

	return "\n  " + formTitle + "\n\n" +
		"  Branch: " + m.input.View() + "\n\n" +
		helpStyle.Render("  enter: create  esc: cancel") + "\n"
}
