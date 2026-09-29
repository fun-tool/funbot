package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:public
var embeddedUI embed.FS

func uiHandler() http.Handler {
	sub, err := fs.Sub(embeddedUI, "public")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
