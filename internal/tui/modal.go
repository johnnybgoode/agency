package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DangerModal is an inline danger confirmation dialog for the sidebar TUI.
// It renders as a block of red-background rows that replace the workspace list
// section when a destructive confirmation is required.
type DangerModal struct {
	Title  string   // bold heading, e.g. "Quit Agency?"
	Lines  []string // body text lines shown below the title
	Prompt string   // action prompt, e.g. "[y] yes   [N] cancel"
	Bg     string   // background color (ANSI), defaults to "9"
	Fg     string   // foreground color (ANSI), defaults to "15"
}

// styles returns the normal and bold lipgloss styles for this modal's colors.
func (d *DangerModal) styles() (normal, bold lipgloss.Style) {
	bg := d.Bg
	if bg == "" {
		bg = "9"
	}
	fg := d.Fg
	if fg == "" {
		fg = "15"
	}
	bgColor := lipgloss.Color(bg)
	fgColor := lipgloss.Color(fg)
	normal = lipgloss.NewStyle().Background(bgColor).Foreground(fgColor)
	bold = lipgloss.NewStyle().Background(bgColor).Foreground(fgColor).Bold(true)
	return normal, bold
}

// Rows returns the styled content rows for the modal. Each row is exactly
// inner columns wide with a colored background that extends to fill the row,
// ready to have "│" appended as the sidebar right border.
func (d *DangerModal) Rows(inner int) []string {
	normal, bold := d.styles()

	fill := func(s string, isBold bool) string {
		style := normal
		if isBold {
			style = bold
		}
		rendered := style.Render(s)
		pad := inner - lipgloss.Width(rendered)
		if pad < 0 {
			pad = 0
		}
		// Extend background to fill the row.
		return rendered + normal.Render(strings.Repeat(" ", pad))
	}

	var rows []string
	rows = append(rows, fill("", false), fill(" ⚠  "+truncate(d.Title, inner-5), true), fill("", false))
	for _, line := range d.Lines {
		rows = append(rows, fill("  "+truncate(line, inner-3), false))
	}
	rows = append(rows, fill("", false), fill("  "+d.Prompt, false), fill("", false))
	return rows
}
