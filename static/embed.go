// Package static embeds the web assets (CSS, JS, and vendored htmx/alpine/
// tailwind/jsbarcode) into the binary, so deploying the POS needs only the
// compiled binary plus a .env — no static/ directory on disk.
package static

import "embed"

//go:embed css js vendor
var Files embed.FS
