package localdb

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Keep the existing column and MySQL JSON format. Other engines use an
// explicit versioned envelope; older empty records receive engine defaults.
type engineOptionsEnvelope struct {
	Version    int                            `json:"version"`
	Kind       connections.Kind               `json:"kind"`
	PostgreSQL *connections.PostgreSQLOptions `json:"postgresql,omitempty"`
	SQLite     *connections.SQLiteOptions     `json:"sqlite,omitempty"`
}

func encodeProfileOptions(p connections.Profile) (string, error) {
	var value any
	switch p.Kind {
	case connections.MySQL:
		value = p.MySQLAdvanced
	case connections.PostgreSQL:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, PostgreSQL: &p.PostgreSQLOptions}
	case connections.SQLite:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, SQLite: &p.SQLiteOptions}
	case connections.ClickHouse:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind}
	default:
		return "", connections.ErrInvalid
	}
	raw, err := json.Marshal(value)
	return string(raw), err
}
func decodeEngineOptions(p *connections.Profile, raw string) error {
	if raw == "" {
		if p.Kind == connections.PostgreSQL {
			p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions()
		} else if p.Kind == connections.SQLite {
			p.SQLiteOptions, _ = connections.NormalizeSQLiteOptions(connections.SQLiteOptions{})
		}
		return nil
	}
	var envelope engineOptionsEnvelope
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 128<<10 || decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Version != 1 || envelope.Kind != p.Kind {
		return fmt.Errorf("invalid saved engine options")
	}
	if p.Kind == connections.PostgreSQL && envelope.PostgreSQL != nil && envelope.SQLite == nil {
		p.PostgreSQLOptions = *envelope.PostgreSQL
		checked, err := connections.NormalizePostgreSQLOptions(p.PostgreSQLOptions)
		p.PostgreSQLOptions = checked
		return err
	}
	if p.Kind == connections.SQLite && envelope.SQLite != nil && envelope.PostgreSQL == nil {
		p.SQLiteOptions = *envelope.SQLite
		checked, err := connections.NormalizeSQLiteOptions(p.SQLiteOptions)
		p.SQLiteOptions = checked
		return err
	}
	if p.Kind == connections.ClickHouse && envelope.SQLite == nil && envelope.PostgreSQL == nil {
		return nil
	}
	return fmt.Errorf("engine options do not match profile")
}
