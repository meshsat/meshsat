package main

import "embed"

// The web app the kits run, built from web/ and copied here by
// Dockerfile.demo (as the kit image copies it to cmd/meshsat/web/dist).
// The committed index.html is a placeholder so the package builds without
// a web build.
//
//go:embed all:web/dist
var embeddedWeb embed.FS
