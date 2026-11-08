package dbruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/cassandra"
	"github.com/nyakang/nyadb/internal/services/connections"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCassandraNativePublicDocumentsCQLAndPublishedGenerations(t *testing.T) {
	if os.Getenv("NYADB_CASSANDRA_WORKBENCH_IT") != "1" {
		t.Skip("requires owned Cassandra workbench fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_CASSANDRA_CONFIG"))
	if err != nil {
		t.Fatal("private probe unavailable")
	}
	var cfg struct {
		Endpoint, Username, Auth, TLSMode string
		CAFile                            string `json:"ca_file"`
		ServerName                        string `json:"server_name"`
		PasswordFile                      string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private config")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("private credential unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	admin, err := cassandra.Open(ctx, connections.CassandraConfig{Endpoint: cfg.Endpoint, Username: cfg.Username, Auth: cfg.Auth, TLSMode: cfg.TLSMode, CAFile: cfg.CAFile, ServerName: cfg.ServerName}, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ks := "nyadb_runtime_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(q string) {
		t.Helper()
		if _, e := admin.Execute(ctx, cassandra.Request{Statement: q}); e != nil {
			t.Fatal(e)
		}
	}
	run("CREATE KEYSPACE " + ks + " WITH replication={'class':'SimpleStrategy','replication_factor':1}")
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, e := admin.Execute(cleanup, cassandra.Request{Statement: "DROP KEYSPACE " + ks}); e != nil {
			t.Error("owned keyspace cleanup", e)
		}
	}()
	run("CREATE TABLE " + ks + ".items(id int PRIMARY KEY,amount decimal)")
	run("INSERT INTO " + ks + ".items(id,amount) VALUES(1,9007199254740993.123456789)")
	u := url.URL{Scheme: "cassandra", Host: cfg.Endpoint, Path: "/" + ks}
	opts := url.Values{"auth": {cfg.Auth}, "sslmode": {cfg.TLSMode}}
	if cfg.Auth == "password" {
		u.User = url.UserPassword(cfg.Username, string(secret))
	}
	if cfg.CAFile != "" {
		opts.Set("ca_file", cfg.CAFile)
	}
	if cfg.ServerName != "" {
		opts.Set("server_name", cfg.ServerName)
	}
	u.RawQuery = opts.Encode()
	parsed, err := connections.ParseURI(u.String())
	if err != nil {
		t.Fatal("public URI")
	}
	p := parsed.Profile
	p.ID = 81
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	if err = m.Test(ctx, p, parsed.Password); err != nil || m.IsConnected(p.ID) {
		t.Fatal("test published runtime", err)
	}
	first, err := m.ConnectSession(ctx, p, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := contract.Require[contract.MetadataProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	ns := m.Namespace(p.ID, ks)
	if ns.ProfileID != p.ID || ns.Catalog != ks || ns.Schema != "" {
		t.Fatal("native keyspace identity")
	}
	objects, err := meta.Browse(ctx, ns)
	if err != nil || len(objects) != 1 || objects[0].Ref != ns.Object("table", "items") {
		t.Fatal("qualified browse", err)
	}
	r := m.Data(p.ID)
	if r.ReserveDocument == nil || r.AdvancedLoader != nil || r.Saver != nil {
		t.Fatal("public data must be document bound")
	}
	a, err := r.ReserveDocument(objects[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ar := a.Runtime()
	if ar.Capabilities.EditableTables || ar.Saver != nil || ar.Importer != nil || ar.AdvancedLoader == nil || ar.JSONL == nil {
		t.Fatal("native read-only capabilities")
	}
	options := contract.DataOptions{Object: objects[0].Ref, UseKeyset: true, Limit: 1}
	page, err := ar.AdvancedLoader(ctx, p.ID, ks, "items", options)
	if err != nil || len(page.Rows) != 1 {
		t.Fatal("public native page", err)
	}
	var out bytes.Buffer
	if err = ar.JSONL(ctx, objects[0].Ref, &out); err != nil || !strings.Contains(out.String(), "9007199254740993.123456789") {
		t.Fatal("public exact export", err)
	}
	qp, err := contract.Require[contract.QueryProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	q, err := qp.ReserveQuery(12)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Release()
	result, err := q.Execute(ctx, ks, "INSERT INTO items(id,amount) VALUES(1,0) IF NOT EXISTS;")
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != "false" || result.AffectedRows != -1 {
		t.Fatal("native LWT acknowledgement", err)
	}
	second, err := m.ConnectSession(ctx, p, parsed.Password)
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal("published generation", err)
	}
	if _, err = ar.AdvancedLoader(ctx, p.ID, ks, "items", options); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old document silently rebound", err)
	}
	if _, err = q.Execute(ctx, ks, "SELECT * FROM items"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old CQL silently rebound", err)
	}
	first.Close()
	a.Close()
	q.Release()
	if !m.IsConnected(p.ID) {
		t.Fatal("original close affected replacement")
	}
	fresh, err := m.Data(p.ID).ReserveDocument(objects[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err = fresh.Runtime().AdvancedLoader(ctx, p.ID, ks, "items", options); err != nil {
		t.Fatal("replacement original target", err)
	}
}
