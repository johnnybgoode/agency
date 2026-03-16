// Package templates embeds the Docker build context for the default agency
// sandbox image (agency:latest) directly into the binary, so the image can be
// built on any machine that has Docker — no local copy of the agency source is
// required.
package templates

import "embed"

// BuildContextFS is an [embed.FS] containing the files needed to build the
// agency:latest image: Dockerfile and docker-entrypoint.sh.
//
//go:embed Dockerfile docker-entrypoint.sh
var BuildContextFS embed.FS
