// Package templates embeds template subdirectories (e.g. docker, agent,
// config) directly into the binary. Each subdirectory of this package is
// accessible via [Sub].
package templates

import (
	"embed"
	"io/fs"
)

// files holds all template subdirectories embedded into the binary.
// Only named subdirectories are embedded to avoid including Go source files.
//
//go:embed docker
var files embed.FS

// Sub returns an [fs.FS] rooted at the named subdirectory (e.g. "docker").
// It returns an error if the subdirectory does not exist in the embedded data.
func Sub(name string) (fs.FS, error) {
	return fs.Sub(files, name)
}
