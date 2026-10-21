package buildtemplates

import (
	"embed"
	"io/fs"
)

// Files contains the built-in Dockerfile build templates.
//
//go:embed */Dockerfile
var Files embed.FS

func Read(name string) ([]byte, error) {
	return fs.ReadFile(Files, name+"/Dockerfile")
}
