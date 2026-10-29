//go:build !windows

package exporter

import "os"

func replaceFile(from, to string) error {
	return os.Rename(from, to)
}
