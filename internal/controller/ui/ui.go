// Package ui contains the compiled web interface.
//
// "make ui" builds the SvelteKit project in web/ and copies the result
// into the dist directory, from where it is compiled into the binary.
package ui

import (
	"embed"
	"io/fs"
)

// The "all:" prefix also embeds files starting with "_" or ".", which
// SvelteKit uses for its "_app" directory.
//
//go:embed all:dist
var dist embed.FS

// Files returns the web interface as a file system rooted at the
// directory that contains index.html.
func Files() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // cannot happen: "dist" is a fixed, embedded directory
	}
	return sub
}
