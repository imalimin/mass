// Package migrations embeds the forward-only schema in the application binary.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

// CurrentVersion is the schema required by the current binary.
const CurrentVersion int64 = 18
