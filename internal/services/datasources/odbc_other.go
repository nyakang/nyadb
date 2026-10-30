//go:build !linux && !darwin && !freebsd && !netbsd && !windows

package datasources

import "context"

func odbcLibraryNames() []string { return nil }
func openODBC(context.Context, Config) (Source, error) {
	return nil, unavailable("ODBC driver manager")
}
func Drivers(context.Context, Config) ([]string, error) {
	return nil, unavailable("ODBC driver manager")
}
