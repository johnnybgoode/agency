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

// renderMascot returns the styled, centered mascot above a fixed-position
// "Creating workspace…" label. The mascot bounces (vertical offset alternates
// per frame) while the label stays pinned at the bottom of the popup.
//
// Layout (height = 9 lines, fitting in an 11-line popup with border):
//
//	Line 0:  blank (bounce padding) or mascot row 1
//	Line 1:  mascot row 1 or 2
//	Line 2:  mascot row 2 or 3
//	Line 3:  mascot row 3 or blank
//	Line 4:  blank (bounce padding) or blank
//	...
//	Line 7:  "Creating workspace…"
//	Line 8:  blank (bottom padding)
func renderMascot(frame, width int) string {
	mascot := mascotFrame()
	mascotLines := strings.Split(mascot, "\n")

	label := loadingText.Render("Creating workspace…")

	// Total content height: 9 lines.
	// Mascot occupies lines 0-4 (3 mascot rows + 2 for bounce padding).
	// Lines 5-6 are spacers, line 7 is the label, line 8 is bottom padding.
	const totalHeight = 9

	lines := make([]string, totalHeight)

	// Bounce: baseline at row 1, bounce down to row 2 on odd frames.
	// Row 0 is always blank to avoid clipping the popup top border.
	offset := 1
	if frame%2 == 1 {
		offset = 2
	}

	// Place mascot rows.
	for i, ml := range mascotLines {
		lines[offset+i] = ml
	}

	// Fixed label near the bottom (line 7), with 1 line of padding below (line 8).
	lines[totalHeight-2] = label

	// Center each line horizontally.
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
