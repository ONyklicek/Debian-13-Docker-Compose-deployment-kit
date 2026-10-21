package presets

import "embed"

// Files contains the built-in app.env templates.
//
//go:embed */app.env */env
var Files embed.FS
