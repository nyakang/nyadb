//go:build windows

package native

import "golang.org/x/sys/windows"

func openLibrary(name string) (uintptr, error) {
	h, e := windows.LoadLibraryEx(name, 0, windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS|windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR)
	return uintptr(h), e
}
func symbol(h uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(h), name)
}
func closeLibrary(h uintptr) { _ = windows.FreeLibrary(windows.Handle(h)) }
