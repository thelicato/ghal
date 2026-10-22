package web

import "embed"

// Assets contains the HTML templates and static assets embedded in the Go binary.
//
//go:embed templates/*.html static/*
var Assets embed.FS
