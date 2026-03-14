// Package tui implements the terminal user interface using bubbletea.
package tui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/johnnybgoode/agency/internal/worktree"
)

// createModel is the Bubble Tea model for the "new workspace" form.
// It has two fields: Name and Branch (with Branch auto-derived from Name).
type createModel struct {
	nameInput    textinput.Model
	branchInput  textinput.Model
	focusedField int  // 0 = name, 1 = branch
	branchEdited bool // true once the user manually edits the branch field
	projectName  string
	submitted    bool
	canceled     bool
}

// newCreateModel initializes the two-field create form.
func newCreateModel(projectName string) createModel {
	nameInput := textinput.New()
	nameInput.Placeholder = "My Feature"
	nameInput.Focus()

	branchInput := textinput.New()
	branchInput.Placeholder = "project-name/my-feature"
	if projectName != "" {
		branchInput.SetValue(projectName + "/")
	}

	return createModel{
		nameInput:    nameInput,
		branchInput:  branchInput,
		focusedField: 0,
		projectName:  projectName,
	}
}

// Init returns the blink command so the text-input cursor animates.
func (m createModel) Init() tea.Cmd { //nolint:gocritic // bubbletea model must use value receivers
	return textinput.Blink
}

// Update handles keyboard events for the create form.
func (m createModel) Update(msg tea.Msg) (createModel, tea.Cmd) { //nolint:gocritic // bubbletea model must use value receivers
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			m.submitted = true
			return m, nil
		case "esc":
			m.canceled = true
			return m, nil
		case "tab", "shift+tab":
			if m.focusedField == 0 {
				m.focusedField = 1
				m.nameInput.Blur()
				m.branchInput.Focus()
			} else {
				m.focusedField = 0
				m.branchInput.Blur()
				m.nameInput.Focus()
			}
			return m, textinput.Blink
		}
	}

	var cmd tea.Cmd
	if m.focusedField == 0 {
		prevName := m.nameInput.Value()
		m.nameInput, cmd = m.nameInput.Update(msg)
		newName := m.nameInput.Value()
		// Auto-fill branch if user hasn't manually edited it.
		if !m.branchEdited && newName != prevName {
			slug := worktree.Slugify(newName)
			prefix := m.projectName
			if prefix != "" {
				m.branchInput.SetValue(prefix + "/" + slug)
			} else {
				m.branchInput.SetValue(slug)
			}
		}
	} else {
		prevBranch := m.branchInput.Value()
		m.branchInput, cmd = m.branchInput.Update(msg)
		if m.branchInput.Value() != prevBranch {
			m.branchEdited = true
		}
	}

	return m, cmd
}

// Name returns the name field value.
func (m createModel) Name() string { //nolint:gocritic // bubbletea model must use value receivers
	return m.nameInput.Value()
}

// Branch returns the branch field value.
func (m createModel) Branch() string { //nolint:gocritic // bubbletea model must use value receivers
	return m.branchInput.Value()
}

// View renders the create form.
func (m createModel) View() string { //nolint:gocritic // bubbletea model must use value receivers
	formTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render("New Workspace")

	nameLabel := "  Name:   "
	branchLabel := "  Branch: "
	if m.focusedField == 1 {
		branchLabel = lipgloss.NewStyle().Bold(true).Render(branchLabel)
	} else {
		nameLabel = lipgloss.NewStyle().Bold(true).Render(nameLabel)
	}

	return "\n  " + formTitle + "\n\n" +
		nameLabel + m.nameInput.View() + "\n" +
		branchLabel + m.branchInput.View() + "\n\n" +
		helpStyle.Render("  tab: next field   enter: create   esc: cancel") + "\n"
}
