package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Claude mascot colors.
var (
	claudeOrange = lipgloss.Color("208")
	eyeDark      = lipgloss.Color("0") // black for the eye upper half
)

// Lipgloss styles for mascot rendering.
var (
	// orangeBlock renders solid orange characters (█ and ▄ edge blocks).
	orangeBlock = lipgloss.NewStyle().Foreground(claudeOrange)
	// halfBlockEye renders ▄ with orange foreground on dark background
	// (top=dark eye, bottom=orange) — used for the eye positions.
	halfBlockEye = lipgloss.NewStyle().Foreground(claudeOrange).Background(eyeDark)
	// loadingText style for the "Creating workspace…" label.
	loadingText = lipgloss.NewStyle().Foreground(claudeOrange).Bold(true)
)

// mascotFrame returns the static Claude mascot rendered with half-block
// characters. The bounce animation is handled by renderMascot via vertical offset.
func mascotFrame() string {
	// Row 0-1:  ▄█████▄   (▄ = edge half-block, █ = full orange)
	row1 := orangeBlock.Render("▄") +
		orangeBlock.Render("█████") +
		orangeBlock.Render("▄")

	// Row 2-3:  ██▄██▄█   (▄ at eye positions = dark top, orange bottom)
	row2 := orangeBlock.Render("██") +
		halfBlockEye.Render("▄") +
		orangeBlock.Render("██") +
		halfBlockEye.Render("▄") +
		orangeBlock.Render("█")

	// Row 4-5:   █   █    (legs)
	row3 := " " + orangeBlock.Render("█") +
		"   " +
		orangeBlock.Render("█")

	return fmt.Sprintf("%s\n%s\n%s", row1, row2, row3)
}

// renderMascot returns the styled, centered mascot with "Creating workspace…"
// text below. The frame parameter controls the bounce animation: even frames
// have no top padding, odd frames have 1 line of top padding.
func renderMascot(frame, width int) string {
	mascot := mascotFrame()

	label := loadingText.Render("Creating workspace…")

	// Compose mascot + blank line + label.
	var lines []string
	if frame%2 == 1 {
		lines = append(lines, "") // bounce: extra blank line at top
	}
	lines = append(lines, strings.Split(mascot, "\n")...)
	lines = append(lines, "", label)
	if frame%2 == 0 {
		lines = append(lines, "") // keep total height consistent
	}

	// Center each line horizontally within the given width.
	var centered []string
	for _, line := range lines {
		visWidth := lipgloss.Width(line)
		pad := (width - visWidth) / 2
		if pad < 0 {
			pad = 0
		}
		centered = append(centered, strings.Repeat(" ", pad)+line)
	}

	return strings.Join(centered, "\n") + "\n"
}
