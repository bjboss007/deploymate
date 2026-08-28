// Package web embeds static assets (CSS, fonts, vendored JS) into the
// binary so a DeployMate install is a single file plus a data directory.
package web

import "embed"

//go:embed static
var StaticFS embed.FS
