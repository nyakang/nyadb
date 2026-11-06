package connections

import "fmt"

// DuckDBOptions describes an embedded database instance, not a server DSN.
// Native runtime configuration is shared by all pools opening the same file.
type DuckDBOptions struct {
	Version           int    `json:"version"`
	ReadOnly          bool   `json:"read_only,omitempty"`
	RequireExisting   bool   `json:"require_existing,omitempty"`
	MemoryLimitMB     int    `json:"memory_limit_mb"`
	Threads           int    `json:"threads"`
	MaxConnections    int    `json:"max_connections"`
	SQLMaxConnections int    `json:"sql_max_connections"`
	DefaultSchema     string `json:"default_schema"`
	LibraryFile       string `json:"library_file,omitempty"`
}

func DefaultDuckDBOptions() DuckDBOptions {
	return DuckDBOptions{Version: 1, RequireExisting: true, MemoryLimitMB: 512, Threads: 4, MaxConnections: 8, SQLMaxConnections: 8, DefaultSchema: "main"}
}
func NormalizeDuckDBOptions(a DuckDBOptions) (DuckDBOptions, error) {
	if a == (DuckDBOptions{}) {
		a = DefaultDuckDBOptions()
	}
	if a.Version != 1 || a.MemoryLimitMB < 64 || a.MemoryLimitMB > 1048576 || a.Threads < 1 || a.Threads > 256 || a.MaxConnections < 1 || a.MaxConnections > 64 || a.SQLMaxConnections < 1 || a.SQLMaxConnections > 64 {
		return a, fmt.Errorf("%w: invalid DuckDB runtime limits", ErrInvalid)
	}
	if a.DefaultSchema == "" || !validEngineText(a.DefaultSchema, 4096) || !validEngineText(a.LibraryFile, 4096) {
		return a, fmt.Errorf("%w: invalid DuckDB schema or runtime path", ErrInvalid)
	}
	return a, nil
}
