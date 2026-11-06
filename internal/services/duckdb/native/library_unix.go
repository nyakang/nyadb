//go:build linux || darwin

package native

import "github.com/ebitengine/purego"

func openLibrary(name string) (uintptr, error) {
	return purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL)
}
func symbol(h uintptr, name string) (uintptr, error) { return purego.Dlsym(h, name) }
func closeLibrary(h uintptr)                         { _ = purego.Dlclose(h) }
