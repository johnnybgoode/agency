package sandbox

import (
	"strings"
	"testing"
)

func TestValidateSandboxName_Valid(t *testing.T) {
	valid := []string{
		"agency-sandbox",
		"my.sandbox",
		"sandbox123",
		"A1b2C3",
		"sandbox+test",
		"sandbox_name",
		strings.Repeat("a", 128),
	}
	for _, name := range valid {
		if err := ValidateSandboxName(name); err != nil {
			t.Errorf("ValidateSandboxName(%q) unexpected error: %v", name, err)
		}
	}
}

func TestValidateSandboxName_Invalid(t *testing.T) {
	tests := []struct {
		name      string
		sandboxID string
	}{
		{"empty string", ""},
		{"starts with dash", "-bad-name"},
		{"starts with dot", ".bad-name"},
		{"too long (129 chars)", strings.Repeat("a", 129)},
		{"shell injection attempt", "sandbox; rm -rf /"},
		{"path traversal attempt", "../../../etc/passwd"},
		{"whitespace", "sandbox name"},
		{"contains slash", "sandbox/name"},
		{"contains null byte", "sandbox\x00name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateSandboxName(tt.sandboxID); err == nil {
				t.Errorf("ValidateSandboxName(%q) expected error, got nil", tt.sandboxID)
			}
		})
	}
}
