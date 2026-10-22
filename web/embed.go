package web

import "embed"

// Assets contains the frontend compiled by pnpm build.
//
//go:embed all:dist
var Assets embed.FS
