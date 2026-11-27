package prestosql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

func TestPrestoSQLPrivateResultDecoderNative(t *testing.T) {
	if os.Getenv("NYADB_PRESTOSQL_PLUGIN_IT") != "1" {
		t.Skip("explicit owned original PrestoSQL350 fixture required")
	}
	var f struct{ Endpoint, Archive, SHA256, Directory string }
	raw, err := os.ReadFile(os.Getenv("NYADB_PRESTOSQL_PLUGIN_FIXTURE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("original fixture unavailable")
	}
	u, err := url.Parse(f.Endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("owned loopback fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	store, err := driverstore.New(filepath.Join(f.Directory, "decoder-store"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Install(ctx, archive, f.SHA256)
	archive.Close()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(ctx, manifest.Key, manifest.Version, f.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	observe := func(pid int) error {
		file, err := os.OpenFile(filepath.Join(f.Directory, "go-processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		return json.NewEncoder(file).Encode(map[string]any{"pid": pid, "test": t.Name()})
	}
	runtime, startup, err := lease.StartJDBCPlugin(ctx, "prestosql", map[string]any{
		"connection_string": "jdbc:presto://" + u.Host + "/memory/default", "username": "nyadb_decoder",
	}, jdbcplugin.Options{}, observe)
	if err != nil || !startup.Connect.ExecutorAcknowledged {
		t.Fatal("original managed connection", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	var evidence []map[string]any
	execute := func(sql string, maxRows int) queryResult {
		t.Helper()
		queryCtx, queryCancel := context.WithTimeout(ctx, 30*time.Second)
		defer queryCancel()
		r, err := runtime.Client.Execute(queryCtx, jdbcplugin.Query{SQL: sql, Catalog: "memory", Schema: "default", MaxRows: maxRows})
		if err != nil {
			t.Fatal("original typed executor", err)
		}
		result, err := decodeResult(r)
		if err != nil {
			t.Fatal("original result decoder", err)
		}
		original := []byte(r.OriginalResponse())
		digest := sha256.Sum256(original)
		evidence = append(evidence, map[string]any{"sql": sql, "receipt": r, "native_bytes": original,
			"native_sha256": hex.EncodeToString(digest[:]), "columns": result.columns, "rows": result.rows,
			"affected_rows": result.affectedRows, "truncated": result.truncated})
		return result
	}
	values := execute("SELECT BIGINT '9223372036854775807' AS x, DECIMAL '9007199254740993.12345678901234567890' AS x, X'00ff80', CAST(NULL AS VARCHAR), '0xnot_binary'", 10)
	if len(values.rows) != 1 || len(values.columns) != 5 || values.columns[0] != values.columns[1] {
		t.Fatal("duplicate labels or original row lost")
	}
	for i, want := range []string{"9223372036854775807", "9007199254740993.1234567890123456789", `"0x00ff80"`, "null", `"0xnot_binary"`} {
		if string(values.rows[0][i]) != want {
			t.Fatal("native value changed", i, string(values.rows[0][i]))
		}
	}
	bounded := execute("SELECT x FROM (VALUES 1,2,3,4) AS t(x)", 3)
	if len(bounded.rows) != 3 || !bounded.truncated {
		t.Fatal("native result truncation lost")
	}
	empty := execute("SELECT 1 WHERE false", 10)
	if len(empty.rows) != 0 || len(empty.columns) != 1 || empty.truncated {
		t.Fatal("empty result lost column identity")
	}
	execute("CREATE TABLE memory.default.decoder_native64 (id BIGINT)", 10)
	written := execute("INSERT INTO memory.default.decoder_native64 VALUES (64)", 10)
	if written.affectedRows != 1 || len(written.columns) != 0 || len(written.rows) != 0 || written.truncated {
		t.Fatal("original native mutation count changed")
	}
	read := execute("SELECT id FROM memory.default.decoder_native64", 10)
	if len(read.rows) != 1 || string(read.rows[0][0]) != "64" {
		t.Fatal("original native write not observed")
	}
	execute("DROP TABLE memory.default.decoder_native64", 10)
	if err := runtime.Close(); err != nil || runtime.Client.Alive() {
		t.Fatal("original executor not closed", err)
	}
	if err := store.Remove(ctx, manifest.Key, manifest.Version); err != nil {
		t.Fatal("original package not released", err)
	}
	raw, err = json.MarshalIndent(evidence, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(f.Directory, "go-decoder.json"), raw, 0600) != nil {
		t.Fatal("original decoder evidence unavailable", err)
	}
}
