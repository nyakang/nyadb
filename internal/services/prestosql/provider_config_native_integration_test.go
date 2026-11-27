package prestosql

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestPrestoSQLPrivateProviderNativeOriginalParameterEncoding(t *testing.T) {
	f, p, c, e := nativeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	source := "nyadb provider+%=64"
	c.JDBCParameters = url.Values{"source": {source}}.Encode()
	s, err := p.OpenManaged(ctx, 64, c, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	q, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	result := queryNative(t, q, "", "SELECT 70 /* provider_config64 */")
	rows := providerHTTP(t, f.Endpoint, "SELECT source FROM system.runtime.queries WHERE query='SELECT 70 /* provider_config64 */'")
	var actual string
	if len(rows) != 1 || len(rows[0]) != 1 || json.Unmarshal(rows[0][0], &actual) != nil || actual != source {
		t.Fatal("native JDBC350 parameter text changed", rows)
	}
	fileResult, err := q.(contract.QueryFileLease).ExecuteFile(ctx, "", strings.NewReader("SELECT 'a;b' AS quoted; /* ; */ SELECT 73"))
	if err != nil || len(fileResult.Steps) != 2 || len(fileResult.Sets) != 2 || fileResult.Sets[0].Rows[0][0].Text != "a;b" || fileResult.Sets[1].Rows[0][0].Text != "73" || fileResult.NativeJSON == "" {
		t.Fatal("original SQL file statements or receipts lost", err)
	}
	bounded := queryNative(t, q, "", "SELECT orderkey FROM tpch.tiny.orders")
	if len(bounded.Sets) != 1 || len(bounded.Sets[0].Rows) != 10000 || !bounded.Truncated || len(bounded.Sets[0].Truncation) != 1 || bounded.Sets[0].Truncation[0] != "native_rows" {
		t.Fatal("original SQL preview row bound not preserved")
	}
	// The pinned plugin cannot send arbitrary Properties to this driver.
	// Refuse an unrepresentable ampersand before it can introduce another key.
	invalid := c
	invalid.JDBCParameters = url.Values{"source": {"prefix&SSL=true"}}.Encode()
	if opened, err := p.OpenManaged(ctx, 64, invalid, Credentials{}); opened != nil || !errors.Is(err, connections.ErrInvalid) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatal("ambiguous public property launched original JVM", err)
	}
	if opened, err := p.OpenManaged(ctx, 64, c, Credentials{Parameters: map[string]string{"extraCredentials": "key:value&SSL=true"}}); opened != nil || !errors.Is(err, connections.ErrInvalid) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatal("ambiguous secret property launched original JVM", err)
	}
	e.mu.Lock()
	count := len(e.processes)
	e.mu.Unlock()
	if count != 2 {
		t.Fatal("invalid properties reached original runtime", count)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
	providerProof(t, f, "provider-original-parameters.json", map[string]any{"configured_source": source, "independent_server_source": actual, "result": result, "sql_file": fileResult, "bounded_preview_rows": len(bounded.Sets[0].Rows), "bounded_native_receipt": bounded.NativeJSON, "ambiguous_values_refused_before_launch": true, "processes": count, "closed_package_removed": true})
}
