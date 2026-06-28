// Package dashboard embeds the live Claude2Kiro web dashboard (served by the
// proxy at /dashboard). It polls /credits and /v1/models client-side, giving a
// real-time credit view that Claude Desktop's own UI doesn't expose.
package dashboard

import _ "embed"

//go:embed dashboard.html
var html string

// HTML returns the dashboard page markup.
func HTML() string {
	return html
}
