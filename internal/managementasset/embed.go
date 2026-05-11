package managementasset

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:web_static
var embeddedWebFS embed.FS

func GetEmbeddedFileSystem() http.FileSystem {
	sub, err := fs.Sub(embeddedWebFS, "web_static")
	if err != nil {
		return nil
	}
	return http.FS(sub)
}

func GetEmbeddedFS() fs.FS {
	sub, err := fs.Sub(embeddedWebFS, "web_static")
	if err != nil {
		return nil
	}
	return sub
}

func HasEmbeddedAssets() bool {
	fsys := GetEmbeddedFileSystem()
	if fsys == nil {
		return false
	}
	f, err := fsys.Open("index.html")
	if err != nil {
		return false
	}
	defer func() {
		_ = f.Close()
	}()
	return true
}
