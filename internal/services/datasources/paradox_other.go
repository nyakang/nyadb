//go:build !linux && !darwin && !freebsd && !netbsd && !windows

package datasources

import "context"

func paradoxLibraryNames() []string                       { return nil }
func openParadox(context.Context, Config) (Source, error) { return nil, unavailable("libpx") }
