package driverstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

// JDBCPluginRuntime pins the external plugin, JRE and JDBC graph until the
// captured process has exited. Each runtime owns one default JDBC connection.
type JDBCPluginRuntime struct {
	Client *jdbcplugin.Client
	lease  *Lease
	once   sync.Once
	err    error
}

// StartJDBCPlugin snapshots connection values and injects only the driver graph
// from this immutable lease. It does not use the database-agent protocol or
// release a package while its original executor is still running.
func (l *Lease) StartJDBCPlugin(ctx context.Context, profile string, connection map[string]any, options jdbcplugin.Options, started func(int) error) (*JDBCPluginRuntime, jdbcplugin.StartupReceipt, error) {
	var startup jdbcplugin.StartupReceipt
	m := l.Manifest()
	if m.Runtime != "java" || m.MainClass != "app.dbx.jdbc.DbxJdbcPlugin" || m.JDBC == nil || connection == nil {
		return nil, startup, ErrPackage
	}
	for _, key := range []string{"jdbc_driver_class", "jdbc_driver_paths"} {
		if _, exists := connection[key]; exists {
			return nil, startup, ErrPackage
		}
	}
	raw, err := json.Marshal(connection)
	if err != nil {
		return nil, startup, ErrPackage
	}
	var bound map[string]json.RawMessage
	if json.Unmarshal(raw, &bound) != nil {
		return nil, startup, ErrPackage
	}
	class, paths, err := l.JDBC()
	if err != nil {
		return nil, startup, err
	}
	bound["jdbc_driver_class"], _ = json.Marshal(class)
	bound["jdbc_driver_paths"], _ = json.Marshal(paths)
	spec, err := l.prepareStart(ctx, profile, nil, false, started)
	if err != nil {
		return nil, startup, err
	}
	client, startup, err := jdbcplugin.StartWithReceipt(ctx, spec, bound, options)
	if err != nil {
		return nil, startup, errors.Join(err, l.releaseRuntime())
	}
	return &JDBCPluginRuntime{Client: client, lease: l}, startup, nil
}

func (r *JDBCPluginRuntime) Close() error {
	r.once.Do(func() {
		r.err = errors.Join(r.Client.Close(), r.lease.releaseRuntime())
	})
	return r.err
}
