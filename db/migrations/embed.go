// Package migrations embeds the forward-only SQL migrations so every binary
// (api, worker, scheduler, migrate CLI) ships the exact schema it was built against.
package migrations

import "embed"

// FS holds the numbered *.up.sql files. Down migrations are intentionally absent:
// production fixes are forward-only (ADR-016).
//
//go:embed *.up.sql
var FS embed.FS
