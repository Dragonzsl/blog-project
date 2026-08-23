package admin

import "embed"

// Files contains the server-rendered owner interface.
//
//go:embed templates/*.html static/*.css static/*.js
var Files embed.FS
