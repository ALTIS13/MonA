package webui

import (
	"embed"
	"io/fs"
)

//go:embed web/*
var embedded embed.FS

func FS() (fs.FS, error) {
	return fs.Sub(embedded, "web")
}

