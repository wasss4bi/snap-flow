package swagger

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/*
var assets embed.FS

func Files() http.FileSystem {
	ui, err := fs.Sub(assets, "ui")
	if err != nil {
		panic(err)
	}
	return http.FS(ui)
}
