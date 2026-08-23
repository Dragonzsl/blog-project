package migrations

import "embed"

// Files contains the forward-only database migration history.
//
//go:embed *.sql
var Files embed.FS
