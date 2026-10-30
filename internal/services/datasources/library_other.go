//go:build !linux && !darwin && !freebsd && !netbsd && !windows

package datasources

func openLibrary(string) (uintptr, error)            { return 0, ErrUnavailable }
func librarySymbol(uintptr, string) (uintptr, error) { return 0, ErrUnavailable }
func closeLibrary(uintptr) error                     { return nil }
