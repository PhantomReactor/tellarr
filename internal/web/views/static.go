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

// AssetVersion is appended to static asset URLs as ?v=... to bust browser
// caches after an update. Bump it whenever css/js content changes.
const AssetVersion = "3"
