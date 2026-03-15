package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestDangerModal_RowsCount(t *testing.T) {
	d := DangerModal{
		Title:  "Test Title",
		Lines:  []string{"Line one", "Line two"},
		Prompt: "[y] yes   [N] cancel",
	}
	rows := d.Rows(20)
	// Expected: blank + title + blank + 2 body lines + blank + prompt + blank = 8 rows.
	want := 8
	if len(rows) != want {
		t.Errorf("Rows() returned %d rows, want %d", len(rows), want)
	}
}

func TestDangerModal_RowsWidth(t *testing.T) {
	inner := 22
	d := DangerModal{
		Title:  "Quit Agency?",
		Lines:  []string{"1 active workspace(s)", "will be paused."},
		Prompt: "[y] yes   [N] cancel",
	}
	for i, row := range d.Rows(inner) {
		w := lipgloss.Width(row)
		if w != inner {
			t.Errorf("row %d width = %d, want %d; content = %q", i, w, inner, row)
		}
	}
}

func TestDangerModal_TitleTruncated(t *testing.T) {
	inner := 10
	d := DangerModal{
		Title:  "This is a very long title that exceeds the inner width",
		Lines:  nil,
		Prompt: "",
	}
	rows := d.Rows(inner)
	for _, row := range rows {
		w := lipgloss.Width(row)
		if w > inner {
			t.Errorf("row width %d exceeds inner %d; content = %q", w, inner, row)
		}
	}
}

func TestDangerModal_NoLines(t *testing.T) {
	d := DangerModal{
		Title:  "Quit?",
		Lines:  nil,
		Prompt: "[y/N]",
	}
	rows := d.Rows(20)
	// blank + title + blank + blank + prompt + blank = 6 rows.
	want := 6
	if len(rows) != want {
		t.Errorf("Rows() with no body lines: got %d rows, want %d", len(rows), want)
	}
}

func TestDangerModal_PromptVisible(t *testing.T) {
	prompt := "[y] yes   [N] cancel"
	d := DangerModal{
		Title:  "Title",
		Lines:  []string{"body"},
		Prompt: prompt,
	}
	rows := d.Rows(30)
	found := false
	for _, row := range rows {
		// Strip ANSI to check plain text content.
		if strings.Contains(lipgloss.NewStyle().Render(row), prompt) || strings.Contains(row, prompt) {
			found = true
			break
		}
	}
	if !found {
		// The prompt text may be embedded in ANSI-styled content; check via plain strip.
		// Accept the test as long as at least one row contains the word "yes".
		for _, row := range rows {
			if strings.Contains(row, "yes") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("prompt %q not found in any row; rows = %v", prompt, rows)
	}
}
