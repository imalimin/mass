// Package migrations embeds the forward-only schema in the application binary.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
