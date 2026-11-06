package connections

import (
	"fmt"
	"strings"
)

// OracleOptions contains public connection settings. Database identifies the
// service or SID; authentication secrets remain in the injected credential vault.
type OracleOptions struct {
	Version               int    `json:"version"`
	ConnectionType        string `json:"connection_type"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	MaxConnections        int    `json:"max_connections"`
	SQLMaxConnections     int    `json:"sql_max_connections"`
	DefaultSchema         string `json:"default_schema,omitempty"`
	ApplicationName       string `json:"application_name"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
}

func DefaultOracleOptions() OracleOptions {
	return OracleOptions{Version: 1, ConnectionType: "service", ConnectTimeoutSeconds: 10, MaxConnections: 8, SQLMaxConnections: 8, ApplicationName: "NyaDB"}
}

func NormalizeOracleOptions(a OracleOptions) (OracleOptions, error) {
	if a == (OracleOptions{}) {
		a = DefaultOracleOptions()
	}
	a.ConnectionType = strings.ToLower(strings.TrimSpace(a.ConnectionType))
	if a.Version != 1 || a.ConnectionType != "service" && a.ConnectionType != "sid" || a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 3600 || a.MaxConnections < 1 || a.MaxConnections > 64 || a.SQLMaxConnections < 1 || a.SQLMaxConnections > 64 {
		return a, fmt.Errorf("%w: invalid Oracle connection mode or limits", ErrInvalid)
	}
	for _, v := range []string{a.DefaultSchema, a.ApplicationName, a.CAFile, a.ServerName} {
		if !validEngineText(v, 4096) {
			return a, fmt.Errorf("%w: invalid Oracle option text", ErrInvalid)
		}
	}
	if len([]byte(a.DefaultSchema)) > 128 || len([]byte(a.ApplicationName)) > 128 || strings.ContainsAny(a.ApplicationName, "()=") {
		return a, fmt.Errorf("%w: invalid Oracle schema/application name", ErrInvalid)
	}
	return a, nil
}
