//go:build linux || darwin || freebsd || netbsd

package datasources

import "github.com/ebitengine/purego"

func openLibrary(name string) (uintptr, error) {
	return purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL)
}
func librarySymbol(h uintptr, name string) (uintptr, error) { return purego.Dlsym(h, name) }
func closeLibrary(h uintptr) error                          { return purego.Dlclose(h) }
