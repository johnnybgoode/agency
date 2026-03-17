// Package templates embeds template subdirectories (e.g. docker, agent,
// config) directly into the binary. Each subdirectory of this package is
// accessible via [Sub].
package templates

import (
	"embed"
	"io/fs"
)

// files holds every file under this package directory. The .go source files
// end up at the root of the embedded FS, but callers only access named
// subdirectories via Sub, so they are never reachable.
//
//go:embed *
var files embed.FS

// Sub returns an [fs.FS] rooted at the named subdirectory (e.g. "docker").
// It returns an error if the subdirectory does not exist in the embedded data.
func Sub(name string) (fs.FS, error) {
	return fs.Sub(files, name)
}
