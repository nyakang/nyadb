package prestosql

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

var ErrManagedDriver = errors.New("original PrestoSQL managed driver must be installed and explicitly selected")

// Credentials are supplied at open time and excluded from ordinary JSON.
// Each process receives an immutable snapshot; providers never persist them.
type Credentials struct {
	Password   string            `json:"-"`
	Parameters map[string]string `json:"-"`
}

func nativeConnection(config connections.PrestoSQLManagedConfig, credentials Credentials) (map[string]any, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || !utf8.ValidString(credentials.Password) || len(credentials.Password) > 65536 || len(credentials.Parameters) > 4 {
		return nil, connections.ErrInvalid
	}
	params, err := url.ParseQuery(config.JDBCParameters)
	if err != nil {
		return nil, connections.ErrInvalid
	}
	for name, value := range credentials.Parameters {
		if !connections.PrestoSQLSecretParameter(name) || !utf8.ValidString(value) || len(value) > 65536 || strings.ContainsAny(value, "\x00&") {
			return nil, connections.ErrInvalid
		}
		params.Set(name, value)
	}
	if u.Scheme == "https" {
		params.Set("SSL", "true")
	} else {
		params.Set("SSL", "false")
	}
	u.Scheme = "presto"
	u.Path = ""
	if config.Catalog != "" {
		u.Path = "/" + config.Catalog
		if config.Schema != "" {
			u.Path += "/" + config.Schema
		}
	}
	// JDBC350 calls URI.getQuery before splitting on '&', and does not
	// apply form decoding. Preserve spaces with %20 and literal plus with
	// %2B. Ampersands inside property values are rejected before this point;
	// the original plugin has no general JDBC Properties argument for them.
	u.RawQuery = strings.ReplaceAll(params.Encode(), "+", "%20")
	connection := map[string]any{"connection_string": "jdbc:" + u.String(), "username": config.Username,
		"connect_timeout_secs": config.ConnectTimeoutSeconds}
	if credentials.Password != "" {
		connection["password"] = credentials.Password
	}
	return connection, nil
}

type process struct {
	runtime *driverstore.JDBCPluginRuntime
	observe func(string, jdbcplugin.Receipt)
}

func (p *process) observed(method string, receipt jdbcplugin.Receipt) {
	if p.observe != nil {
		p.observe(method, receipt.Clone())
	}
}

func (p *process) query(ctx context.Context, q jdbcplugin.Query) (queryResult, jdbcplugin.Receipt, error) {
	r, executeErr := p.runtime.Client.Execute(ctx, q)
	// Cancellation can race with a successful executor acknowledgement.
	// Decode that acknowledgement before interpreting the caller's error.
	result, err := acknowledgedResult(r, executeErr)
	if errors.Is(err, ErrResponse) {
		_ = p.runtime.Close()
		r.ProcessExitConfirmed = true
	}
	p.observed("executeQuery", r)
	if err != nil && !p.runtime.Client.Alive() {
		err = errors.Join(contract.ErrSQLSessionLost, err)
	}
	return result, r, err
}

func acknowledgedResult(r jdbcplugin.Receipt, executeErr error) (queryResult, error) {
	if executeErr != nil && (!r.ExecutorAcknowledged || r.RemoteFailure || r.Diagnostic != nil) {
		return queryResult{}, operationError(executeErr)
	}
	result, decodeErr := decodeResult(r)
	if decodeErr != nil {
		return queryResult{}, errors.Join(contract.ErrSQLSessionLost, executeErr, decodeErr)
	}
	return result, operationError(executeErr)
}

func operationError(err error) error {
	if errors.Is(err, jdbcplugin.ErrLost) || errors.Is(err, jdbcplugin.ErrClosed) || errors.Is(err, jdbcplugin.ErrProtocol) {
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	return err
}

func (p Provider) start(ctx context.Context, c connections.PrestoSQLManagedConfig, connection map[string]any) (*process, error) {
	if ctx == nil || p.Store == nil {
		return nil, connections.ErrInvalid
	}
	lease, err := p.Store.Acquire(ctx, "prestosql", c.DriverVersion, c.DriverSHA256)
	if err != nil {
		return nil, errors.Join(ErrManagedDriver, err)
	}
	m := lease.Manifest()
	plugin, driver := false, false
	for _, f := range m.Files {
		plugin = plugin || f.Path == "dbx-jdbc-plugin.jar" && f.SHA256 == "f9f6d0d87677f3f5cbb286926006e652ea768d062a1ba829b38ee018f1a50c99"
		driver = driver || f.Path == "jdbc/presto-jdbc-350.jar" && f.SHA256 == "664a23e517c120c1d5a9bb88b215612b117415b9f1649bc22c48298eaf3f979b"
	}
	if !plugin || !driver || m.MainClass != "app.dbx.jdbc.DbxJdbcPlugin" || m.JDBC == nil || m.JDBC.Class != "io.prestosql.jdbc.PrestoDriver" || len(m.Classpath) != 1 || m.Classpath[0] != "dbx-jdbc-plugin.jar" || len(m.JDBC.Classpath) != 1 || m.JDBC.Classpath[0] != "jdbc/presto-jdbc-350.jar" {
		_ = lease.Close()
		return nil, ErrManagedDriver
	}
	connect, cancel := context.WithTimeout(ctx, time.Duration(c.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	runtime, startup, err := lease.StartJDBCPlugin(connect, "prestosql", connection, jdbcplugin.Options{}, p.Started)
	if p.Observe != nil {
		p.Observe("protocol", startup.Protocol.Clone())
		p.Observe("connect", startup.Connect.Clone())
	}
	if err != nil {
		_ = lease.Close()
		return nil, operationError(err)
	}
	original := &process{runtime: runtime, observe: p.Observe}
	// isValid/connectionInfo do not contact this server. This fully qualified
	// SQL request establishes the original network path and actual version.
	result, _, err := original.query(connect, jdbcplugin.Query{
		SQL: "SELECT node_version FROM system.runtime.nodes WHERE coordinator", MaxRows: 2,
		TimeoutSeconds: c.ConnectTimeoutSeconds,
	})
	var version string
	if err == nil && (result.truncated || len(result.rows) != 1 || len(result.rows[0]) != 1 || json.Unmarshal(result.rows[0][0], &version) != nil || version != "350") {
		err = contract.ErrUnsupported
	}
	if err != nil || connect.Err() != nil {
		_ = runtime.Close()
		return nil, errors.Join(err, connect.Err())
	}
	return original, nil
}
