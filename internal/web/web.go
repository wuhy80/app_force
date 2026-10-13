package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

func Assets() fs.FS { sub, _ := fs.Sub(assets, "dist"); return sub }
