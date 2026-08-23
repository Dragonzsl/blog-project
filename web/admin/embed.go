package admin

import "embed"

// Files contains the server-rendered owner interface.
//
//go:embed templates/*.html static/*.css
var Files embed.FS
