//go:build windows

package datasources

import "golang.org/x/sys/windows"

func openLibrary(name string) (uintptr, error) {
	h, e := windows.LoadLibrary(name)
	return uintptr(h), e
}
func librarySymbol(h uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(h), name)
}
func closeLibrary(h uintptr) error { return windows.FreeLibrary(windows.Handle(h)) }
