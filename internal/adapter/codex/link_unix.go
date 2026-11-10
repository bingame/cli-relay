//go:build !windows

package codex

import "os"

func linkNativePath(source, destination string, _ bool) error {
	return os.Symlink(source, destination)
}
