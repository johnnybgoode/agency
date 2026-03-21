package tui

import (
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

// mascotBody returns the top 3 rows shared by all animation frames.
func mascotBody() [3]string {
	return [3]string{
		// Row 1:  █████████  (head top)
		" " + orangeBlock.Render("█████████") + " ",
		// Row 2: ██▄█████▄██ (face with eyes)
		orangeBlock.Render("██") +
			halfBlockEye.Render("▄") +
			orangeBlock.Render("█████") +
			halfBlockEye.Render("▄") +
			orangeBlock.Render("██"),
		// Row 3:  █████████  (body)
		" " + orangeBlock.Render("█████████") + " ",
	}
}

// mascotLegs returns the legs row for the given animation frame index (0-2).
//
//	0: █ █   █ █   (standing)
//	1: █ ▀   ▀ █   (feet out)
//	2: ▀ █   █ ▀   (feet swapped)
func mascotLegs(idx int) string {
	switch idx {
	case 1:
		return " " + orangeBlock.Render("█") + " " +
			orangeBlock.Render("▀") + "   " +
			orangeBlock.Render("▀") + " " +
			orangeBlock.Render("█")
	case 2:
		return " " + orangeBlock.Render("▀") + " " +
			orangeBlock.Render("█") + "   " +
			orangeBlock.Render("█") + " " +
			orangeBlock.Render("▀")
	default:
		return " " + orangeBlock.Render("█") + " " +
			orangeBlock.Render("█") + "   " +
			orangeBlock.Render("█") + " " +
			orangeBlock.Render("█")
	}
}

// mascotFrameIndex maps the animation tick counter to a frame index (0-2).
// Cycle: 0 → 1 → 2 → 1 → 2 → … (frame 0 only on first tick).
func mascotFrameIndex(tick int) int {
	if tick == 0 {
		return 0
	}
	if tick%2 == 1 {
		return 1
	}
	return 2
}

// renderMascot returns the styled, centered mascot with a fixed
// "Creating workspace…" label below. The legs animate through a
// walk cycle while the body and label stay in place.
//
// Layout (height = 8 lines, fitting in an 11-line popup with border):
//
//	Line 0:  blank (top padding)
//	Line 1:  mascot row 1 (head)
//	Line 2:  mascot row 2 (face)
//	Line 3:  mascot row 3 (body)
//	Line 4:  mascot row 4 (legs — animated)
//	Line 5:  blank (spacer)
//	Line 6:  "Creating workspace…"
//	Line 7:  blank (bottom padding)
func renderMascot(frame, width int) string {
	body := mascotBody()
	idx := mascotFrameIndex(frame)
	legs := mascotLegs(idx)

	label := loadingText.Render("Creating workspace…")

	lines := []string{
		"",      // top padding
		body[0], // head
		body[1], // face
		body[2], // body
		legs,    // legs (animated)
		"",      // spacer
		label,   // label
		"",      // bottom padding
	}

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
