//go:build !windows

package download

import "os"

func removeRegularFile(path string) error {
	return os.Remove(path)
}
