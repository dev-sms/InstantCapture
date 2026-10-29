//go:build !windows

package windows

func AcquireSingleInstance(string) (func(), bool, error) {
	return func() {}, false, nil
}
