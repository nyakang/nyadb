//go:build !linux && !darwin && !windows

package native

import "errors"

func openLibrary(string) (uintptr, error) {
	return 0, errors.New("DuckDB native runtime is unavailable on this platform")
}
func symbol(uintptr, string) (uintptr, error) { return 0, errors.New("unsupported platform") }
func closeLibrary(uintptr)                    {}
