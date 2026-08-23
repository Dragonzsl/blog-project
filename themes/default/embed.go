package defaulttheme

import "embed"

// Files contains the built-in, always-available default theme.
//
//go:embed templates/*.html assets/*.css
var Files embed.FS

const Version = "1.0.0"
