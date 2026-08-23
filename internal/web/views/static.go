package views

import "io/fs"
import "embed"

//go:embed static
var staticFS embed.FS

func StaticFS() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
