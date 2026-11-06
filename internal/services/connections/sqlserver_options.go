package connections

import "fmt"

// SQLServerOptions contains public TDS settings. SQL authentication secrets are
// supplied separately; integrated authentication is not silently inferred.
type SQLServerOptions struct {
	Version               int    `json:"version"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	MaxConnections        int    `json:"max_connections"`
	SQLMaxConnections     int    `json:"sql_max_connections"`
	ApplicationName       string `json:"application_name"`
	DefaultSchema         string `json:"default_schema"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
}

func DefaultSQLServerOptions() SQLServerOptions {
	return SQLServerOptions{Version: 1, ConnectTimeoutSeconds: 10, MaxConnections: 8, SQLMaxConnections: 8, ApplicationName: "NyaDB", DefaultSchema: "dbo"}
}

func NormalizeSQLServerOptions(a SQLServerOptions) (SQLServerOptions, error) {
	if a == (SQLServerOptions{}) {
		a = DefaultSQLServerOptions()
	}
	if a.Version != 1 || a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 3600 || a.MaxConnections < 1 || a.MaxConnections > 64 || a.SQLMaxConnections < 1 || a.SQLMaxConnections > 64 {
		return a, fmt.Errorf("%w: invalid SQL Server timeouts or connection limits", ErrInvalid)
	}
	for _, v := range []string{a.ApplicationName, a.DefaultSchema, a.ServerName, a.CAFile} {
		if !validEngineText(v, 4096) {
			return a, fmt.Errorf("%w: invalid SQL Server option text", ErrInvalid)
		}
	}
	if a.DefaultSchema == "" || len([]rune(a.DefaultSchema)) > 128 || len([]rune(a.ApplicationName)) > 128 {
		return a, fmt.Errorf("%w: invalid SQL Server schema/application name", ErrInvalid)
	}
	return a, nil
}
