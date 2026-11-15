package main

import (
	_ "embed"
	"os"
	"path/filepath"
)

// The MCS logo, cut out of its background, for the corner of exported videos.
//
//go:embed logo.png
var builtInLogo []byte

// videoLogo is the logo put on exported videos: a logo.png beside the
// program if there is one, so it can be changed without rebuilding, else the
// one built in. It should have a see-through background.
func videoLogo() []byte {
	if exe, err := os.Executable(); err == nil {
		if data, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "logo.png")); err == nil && len(data) > 0 {
			return data
		}
	}
	return builtInLogo
}
