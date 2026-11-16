// Package web embeds the built front end (web/dist) into berth-panel.
package web

import "embed"

//go:embed all:dist
var FS embed.FS
