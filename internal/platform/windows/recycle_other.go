//go:build !windows

package windows

import "errors"

func MoveFileToRecycleBin(string) error {
	return errors.New("recycle bin is only available on Windows")
}

func MoveFilesToRecycleBin([]string) error {
	return errors.New("recycle bin is only available on Windows")
}
