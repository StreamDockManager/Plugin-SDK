// Package inspectors contains the SDK's packaged, local inspector helpers
package inspectors

import (
	"embed"
	"io/fs"
)

//go:embed *.js *.json display/*.json icons/*.svg
var files embed.FS

// Return independent copies of the embedded inspector assets
func Assets() map[string][]byte {
	out := map[string][]byte{}
	_ = fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := files.ReadFile(name)
			if err != nil {
				return err
			}
			out[name] = data
		}
		return nil
	})
	return out
}
