//go:build windows

package dpi

import "golang.org/x/sys/windows"

func openLibrary(path string) (uintptr, error) {
	h, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS|windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR)
	return uintptr(h), err
}
func symbol(h uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(h), name)
}
func closeLibrary(h uintptr) { _ = windows.FreeLibrary(windows.Handle(h)) }
