package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	// dangerBg is ANSI-256 color 160 — a medium red.
	dangerBg = lipgloss.Color("160")
	// dangerFg is bright white.
	dangerFg    = lipgloss.Color("15")
	dangerStyle = lipgloss.NewStyle().Background(dangerBg).Foreground(dangerFg)
	dangerBold  = lipgloss.NewStyle().Background(dangerBg).Foreground(dangerFg).Bold(true)
)

// DangerModal is an inline danger confirmation dialog for the sidebar TUI.
// It renders as a block of red-background rows that replace the workspace list
// section when a destructive confirmation is required.
type DangerModal struct {
	Title  string   // bold heading, e.g. "Quit Agency?"
	Lines  []string // body text lines shown below the title
	Prompt string   // action prompt, e.g. "[y] yes   [N] cancel"
}

// Rows returns the styled content rows for the modal. Each row is exactly
// inner columns wide with a red background that extends to fill the row,
// ready to have "│" appended as the sidebar right border.
func (d DangerModal) Rows(inner int) []string {
	fill := func(s string, bold bool) string {
		style := dangerStyle
		if bold {
			style = dangerBold
		}
		rendered := style.Render(s)
		pad := inner - lipgloss.Width(rendered)
		if pad < 0 {
			pad = 0
		}
		// Extend red background to fill the row.
		return rendered + dangerStyle.Render(strings.Repeat(" ", pad))
	}

	var rows []string
	rows = append(rows, fill("", false), fill(" ⚠  "+truncate(d.Title, inner-5), true), fill("", false))
	for _, line := range d.Lines {
		rows = append(rows, fill("  "+truncate(line, inner-3), false))
	}
	rows = append(rows, fill("", false), fill("  "+d.Prompt, false), fill("", false))
	return rows
}
