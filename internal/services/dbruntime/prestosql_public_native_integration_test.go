package dbruntime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/testsupport/prestosqlfixture"
)

func TestPrestoSQLNativePublicRuntimeBindingsAndRecovery(t *testing.T) {
	f := prestosqlfixture.Read(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	directory := filepath.Join(f.Directory, t.Name(), "store")
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := store.Import(ctx, driverstore.Product{Key: "prestosql", Runtime: "java", MainClass: "app.dbx.jdbc.DbxJdbcPlugin"}, f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.RemovePinned(context.Background(), driver.Key, driver.Version, driver.SHA256); err != nil {
			t.Error("package still leased", err)
		}
	})
	parsed, err := connections.ParseURI(f.URI(t))
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Profile
	p.ID = 6701
	p.Name = "public runtime"
	p.PrestoSQLOptions.DriverVersion, p.PrestoSQLOptions.DriverSHA256, p.PrestoSQLOptions.DriverDirectory = driver.Version, driver.SHA256, driver.Directory
	m := New()
	RegisterBuiltins(m, nil)
	t.Cleanup(func() { m.Close() })
	ctx = driverstore.WithRuntimeObserver(ctx, f.Observe(directory))
	first, err := m.ConnectSession(ctx, p, f.Secret(t))
	if err != nil {
		t.Fatal("builtin open", err)
	}
	if err := store.RemovePinned(ctx, driver.Key, driver.Version, driver.SHA256); !errors.Is(err, driverstore.ErrBusy) {
		t.Fatal("live builtin package removed", err)
	}
	meta, err := contract.Require[contract.MetadataProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	namespaces, err := meta.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	label := `"memory"."default"`
	namespace := m.Namespace(p.ID, label)
	object := namespace.Object("table", "runtime_public67")
	if object.ProfileID != p.ID || object.Catalog != "memory" || object.Schema != "default" || m.Namespace(p.ID, "") != namespace {
		t.Fatal("qualified identity changed")
	}
	found := false
	for _, n := range namespaces {
		if n.Ref == namespace && n.Label == label {
			found = true
		}
	}
	if !found {
		t.Fatal("qualified metadata unavailable")
	}
	query, err := contract.Require[contract.QueryProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	q, err := query.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Release)
	run := func(sql string) contract.SQLResult {
		t.Helper()
		r, e := q.Execute(ctx, label, sql)
		if e != nil {
			t.Fatal("original query", e)
		}
		return r
	}
	run(`CREATE TABLE memory.default.runtime_public67 AS SELECT BIGINT '9223372036854775807' AS id,rpad('',2049,'猫') AS note,X'00ff80' AS payload`)
	base := contract.ComposeData(first)
	if caps := first.Capabilities(); !caps.EditableTables || !caps.Count || !caps.FieldChunks || !caps.Export || !caps.JSONL {
		t.Fatal("public document capabilities lost", caps)
	}
	if base.ReserveDocument == nil || base.Transactions.Begin != nil || first.Capabilities().Explain || m.Administration(p.ID) != nil {
		t.Fatal("unsupported tools or missing document capability")
	}
	lease, err := base.ReserveDocument(object)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	r := lease.Runtime()
	page, err := r.Loader(ctx, p.ID, label, object.Name, 0, 10)
	if err != nil || len(page.Rows) != 1 || page.Rows[0][0].Text != "9223372036854775807" || len(page.RowRefs) != 1 {
		t.Fatal("public typed page", err)
	}
	chunk, err := r.Chunk(ctx, p.ID, label, object.Name, "note", *page.RowRefs[0], 1024, 1024)
	if err != nil || chunk.Text != strings.Repeat("猫", 1024) || chunk.TotalUnits != 2049 {
		t.Fatal("public original field grant", err)
	}
	exported := 0
	err = r.Exporter(ctx, p.ID, label, object.Name, contract.DataOptions{}, []string{"note"}, func(v []contract.DataValue) error {
		exported++
		if len(v) != 1 || v[0].Text != strings.Repeat("猫", 2049) {
			return errors.New("complete value lost")
		}
		return nil
	})
	if err != nil || exported != 1 {
		t.Fatal("public complete export", err)
	}
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "2"}, "note": {Kind: contract.Text, Text: "inserted"}, "payload": {Kind: contract.Binary, Bytes: []byte{1, 2}}}}}
	plan, err := r.PreviewEdits(ctx, p.ID, label, object.Name, edits)
	if err != nil {
		t.Fatal("public write preview", err)
	}
	result, err := r.SubmitEdits(ctx, p.ID, label, object.Name, plan)
	plan.Close()
	if err != nil || result == nil {
		t.Fatal("public write acknowledgement", err)
	}
	count := run("SELECT count(*) FROM memory.default.runtime_public67")
	if count.Sets[0].Rows[0][0].Text != "2" {
		t.Fatal("public insert did not reach server")
	}
	second, err := m.ConnectSession(ctx, p, f.Secret(t))
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation() <= first.Generation() {
		t.Fatal("public generation failed to advance")
	}
	if _, err := meta.Namespaces(ctx); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old metadata used replacement", err)
	}
	if _, err := q.Execute(ctx, label, "SELECT 99"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old query executed", err)
	}
	if _, err := r.Loader(ctx, p.ID, label, object.Name, 0, 10); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old data owner used replacement", err)
	}
	first.Close()
	if !m.IsConnected(p.ID) {
		t.Fatal("late old close revoked new publication")
	}
	query2, err := contract.Require[contract.QueryProvider](second)
	if err != nil {
		t.Fatal(err)
	}
	q2, err := query2.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Release()
	if _, err := q2.Execute(ctx, label, "DROP TABLE memory.default.runtime_public67"); err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	f.Journal(t, "public-runtime.jsonl", map[string]any{"test": t.Name(), "original_generation": first.Generation(), "replacement_generation": second.Generation(), "object": object, "write": result, "page": page, "chunk": chunk, "exported": exported, "live_removal_refused": true, "old_bindings_revoked": true, "starts": f.Starts.Load()})
}
