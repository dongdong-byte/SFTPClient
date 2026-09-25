//go:build !windows

package transport

import "os"

func removeRegularFile(path string) error {
	return os.Remove(path)
}
