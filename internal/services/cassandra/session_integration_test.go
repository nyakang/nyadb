package cassandra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func sessionFixture(t *testing.T) (connections.CassandraConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_CASSANDRA_SESSION_IT") != "1" {
		t.Skip("requires owned Cassandra session acceptance")
	}
	return nativeFixture(t)
}
func fixtureSession(t *testing.T, cfg connections.CassandraConfig, secret, keyspace string) *Session {
	t.Helper()
	p, _ := connections.Defaults(connections.Cassandra)
	p.ID, p.Name, p.Database = 71, "native session", keyspace
	p.Host, _, _ = net.SplitHostPort(cfg.Endpoint)
	_, port, _ := net.SplitHostPort(cfg.Endpoint)
	n, _ := strconv.Atoi(port)
	p.Port = uint16(n)
	p.Username, p.TLSMode = cfg.Username, cfg.TLSMode
	p.CassandraOptions = connections.CassandraOptions{Version: 1, Auth: cfg.Auth, CAFile: cfg.CAFile, ServerName: cfg.ServerName}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, err := OpenSession(ctx, p, secret)
	if err != nil {
		t.Fatalf("session open: %v", err)
	}
	s := v.(*Session)
	t.Cleanup(func() { s.Close() })
	return s
}
func leaseQuery(t *testing.T, l contract.QueryLease, namespace, source string) contract.SQLResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := l.Execute(ctx, namespace, source)
	if err != nil {
		t.Fatalf("original CQL lease: %v", err)
	}
	return r
}
func fixtureLease(t *testing.T, s *Session, id uint64) contract.QueryLease {
	t.Helper()
	l, err := s.ReserveQuery(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	return l
}

func TestCassandraNativeQualifiedSchemaAndIndependentCQL(t *testing.T) {
	cfg, secret := sessionFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	mixed := "Mixed_" + ks
	query(t, c, Request{Statement: "CREATE KEYSPACE " + quoteName(mixed) + " WITH replication={'class':'SimpleStrategy','replication_factor':1}"})
	t.Cleanup(func() {
		cleanup := c
		if !cleanup.Alive() {
			cleanup = fixtureClient(t, cfg, secret)
			defer cleanup.Close()
		}
		query(t, cleanup, Request{Statement: "DROP KEYSPACE " + quoteName(mixed)})
	})
	for _, name := range []string{ks, mixed} {
		q := quoteName(name)
		query(t, c, Request{Statement: "CREATE TYPE " + q + ".payload (empty text, missing int)"})
		query(t, c, Request{Statement: "CREATE TABLE " + q + ".rows (tenant text,bucket int,id int,stamp timestamp,memo text static,amount decimal,payload frozen<payload>,pair tuple<text,int>,PRIMARY KEY ((tenant,bucket),id,stamp)) WITH CLUSTERING ORDER BY (id DESC,stamp ASC) AND comment='native metadata'"})
		query(t, c, Request{Statement: "CREATE INDEX rows_amount ON " + q + ".rows(amount)"})
	}
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".view_base (id int PRIMARY KEY,n int)"})
	query(t, c, Request{Statement: "CREATE MATERIALIZED VIEW " + ks + ".rows_view AS SELECT * FROM " + ks + ".view_base WHERE id IS NOT NULL PRIMARY KEY (id)"})
	s := fixtureSession(t, cfg, secret, mixed)
	if s.Identity().HostID != c.Identity().HostID || s.Capabilities().EditableTables {
		t.Fatal("original node or private capability lost")
	}
	names, err := s.Namespaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, n := range names {
		if n.Ref.ProfileID != 71 || n.Ref.Catalog != n.Label || n.Ref.Schema != "" {
			t.Fatal("unqualified namespace")
		}
		found[n.Label] = true
	}
	if !found[ks] || !found[mixed] {
		t.Fatal("case-sensitive keyspace missing")
	}
	var ids []string
	for _, name := range []string{ks, mixed} {
		objects, err := s.Browse(context.Background(), s.Namespace(name))
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]bool{}
		for _, v := range objects {
			kinds[v.Ref.Kind] = true
			if v.Ref.Catalog != name || !json.Valid([]byte(v.Definition)) {
				t.Fatal("native object definition missing")
			}
		}
		if !kinds["table"] || !kinds["type"] || name == ks && !kinds["view"] {
			t.Fatal("native categories missing")
		}
		m, err := s.TableMetadata(context.Background(), s.Namespace(name).Object("table", "rows"))
		if err != nil {
			t.Fatal(err)
		}
		if m.Editable || m.Engine != "Cassandra" || m.Comment != "native metadata" || len(m.Columns) != 8 || len(m.Indexes) != 2 {
			t.Fatal("native table metadata lost")
		}
		for i, want := range []string{"tenant", "bucket", "id", "stamp", "memo"} {
			if m.Columns[i].Name != want {
				t.Fatal("partition/clustering/static order lost")
			}
		}
		if m.Columns[0].Key != "partition_key" || m.Columns[2].Key != "clustering" || m.Indexes[0].Columns[2].Direction != "desc" || m.Indexes[0].Columns[3].Direction != "asc" {
			t.Fatal("native key roles lost")
		}
		var row map[string]encodedValue
		if json.Unmarshal([]byte(m.Definition), &row) != nil || row["id"].Value == nil {
			t.Fatal("actual table UUID missing")
		}
		ids = append(ids, *row["id"].Value)
		udt, err := s.TableMetadata(context.Background(), s.Namespace(name).Object("type", "payload"))
		if err != nil || len(udt.Columns) != 2 || udt.Columns[0].Name != "empty" || udt.Columns[1].Name != "missing" {
			t.Fatal("UDT field order/type lost", err)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("same-named tables rebound across keyspaces")
	}
	if _, err := s.TableMetadata(context.Background(), s.Namespace(ks).Object("view", "rows_view")); err != nil {
		t.Fatal(err)
	}
	wrong := s.Namespace(ks).Object("table", "rows")
	wrong.ProfileID++
	if _, err := s.TableMetadata(context.Background(), wrong); err == nil {
		t.Fatal("foreign identity accepted")
	}
	l1, l2 := fixtureLease(t, s, 1), fixtureLease(t, s, 2)
	if err := l1.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l2.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l1.(*queryLease).client == l2.(*queryLease).client || l1.(*queryLease).client == s.client {
		t.Fatal("physical CQL leases shared")
	}
	change := leaseQuery(t, l1, "", "CREATE TYPE acknowledgement_payload (value int)")
	var nativeChange SchemaChange
	if len(change.Sets[0].Columns) != 5 || change.Sets[0].Rows[0][4].Kind != contract.JSON || json.Unmarshal([]byte(change.Sets[0].Rows[0][4].Text), &nativeChange) != nil || nativeChange.Target != "TYPE" || nativeChange.Keyspace != mixed || nativeChange.Name != "acknowledgement_payload" {
		t.Fatal("full native schema-change acknowledgement lost")
	}
	r := leaseQuery(t, l1, "", "BEGIN UNLOGGED BATCH INSERT INTO rows(tenant,bucket,id,stamp,amount,pair) VALUES ('a',1,1,0,12345678901234567890.123456789,('',7)); INSERT INTO rows(tenant,bucket,id,stamp,amount) VALUES ('a',1,2,0,2.5); APPLY BATCH; SELECT amount,pair FROM rows WHERE tenant='a' AND bucket=1 AND id=1")
	if len(r.Steps) != 2 || r.AffectedRows != -1 || r.Sets[0].Rows[0][0].Text != "VOID" || r.Sets[1].Rows[0][0].Kind != contract.Decimal || r.Sets[1].Rows[0][0].Text != "12345678901234567890.123456789" || r.Sets[1].Rows[0][1].Kind != contract.JSON {
		t.Fatal("CQL acknowledgement or precision lost")
	}
	leaseQuery(t, l1, "", "USE "+quoteName(ks))
	if got := leaseQuery(t, l1, "", "SELECT tenant FROM rows"); len(got.Rows) != 0 {
		t.Fatal("USE did not retain original physical namespace")
	}
	if got := leaseQuery(t, l2, "", "SELECT tenant FROM rows"); len(got.Rows) != 2 {
		t.Fatal("one CQL lease changed another")
	}
	file, err := l1.(contract.QueryFileLease).ExecuteFile(context.Background(), ks, strings.NewReader("INSERT INTO rows(tenant,bucket,id,stamp,amount) VALUES ('f',1,3,0,8.75); SELECT amount FROM rows WHERE tenant='f' AND bucket=1"))
	if err != nil || len(file.Steps) != 2 || file.Sets[1].Rows[0][0].Text != "8.75" {
		t.Fatal("native CQL file failed", err)
	}
	lwt := leaseQuery(t, l1, "", "INSERT INTO rows(tenant,bucket,id,stamp,amount) VALUES ('f',1,3,0,99.5) IF NOT EXISTS")
	if lwt.Sets[0].Columns[0].Name != "[applied]" || lwt.Sets[0].Rows[0][0].Kind != contract.Boolean || lwt.Sets[0].Rows[0][0].Text != "false" || lwt.AffectedRows != -1 {
		t.Fatal("LWT result incorrectly treated as affected rows")
	}
	if _, err := l1.Execute(context.Background(), "", "invalid CQL syntax"); errors.Is(err, contract.ErrSQLSessionLost) || err == nil {
		t.Fatal("complete syntax error lost physical lease")
	}
	leaseQuery(t, l1, "", "SELECT tenant FROM rows")
}

func TestCassandraNativeCQLPagingStreamingAndAcknowledgement(t *testing.T) {
	cfg, secret := sessionFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".pages (bucket int,id int,n bigint,p blob,PRIMARY KEY(bucket,id))"})
	s := fixtureSession(t, cfg, secret, ks)
	l := fixtureLease(t, s, 11)
	const total = 5103
	for start := 0; start < total; start += 64 {
		var b strings.Builder
		b.WriteString("BEGIN UNLOGGED BATCH ")
		for i := start; i < start+64 && i < total; i++ {
			fmt.Fprintf(&b, "INSERT INTO pages(bucket,id,n,p) VALUES(1,%d,9223372036854775807,0x00ff);", i)
		}
		b.WriteString("APPLY BATCH")
		leaseQuery(t, l, "", b.String())
	}
	preview := leaseQuery(t, l, "", "SELECT id,n,p FROM pages WHERE bucket=1")
	if len(preview.Rows) != 5000 || !preview.Truncated {
		t.Fatal("bounded multi-page CQL preview failed")
	}
	count := 0
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := l.ExecuteStream(ctx, "", "SELECT id,n,p FROM pages WHERE bucket=1", func(set int, columns []contract.Column, cells []contract.Cell) error {
		if set != 0 || cells[1].Kind != contract.Integer || cells[1].Text != "9223372036854775807" || !bytes.Equal(cells[2].Bytes, []byte{0, 255}) {
			return errors.New("typed streamed value changed")
		}
		columns[0].Name = "mutated callback"
		cells[2].Bytes[0] = 99
		count++
		return nil
	})
	if err != nil || count != total || len(stream.Sets[0].Rows) != 5000 || !stream.Truncated || stream.Sets[0].Columns[0].Name != "id" || stream.Sets[0].Rows[0][2].Bytes[0] != 0 {
		t.Fatal("stream truncated or callback mutated preview", err)
	}
	want := errors.New("consumer stopped")
	partial, err := l.ExecuteStream(ctx, "", "SELECT id FROM pages WHERE bucket=1", func(int, []contract.Column, []contract.Cell) error { return want })
	if !errors.Is(err, want) || len(partial.Steps) != 1 || partial.Steps[0].State != "completed" {
		t.Fatal("callback error lost")
	}
	leaseQuery(t, l, "", "SELECT id FROM pages WHERE bucket=1 LIMIT 1")
}

func TestCassandraNativeLeaseLostCounterReplyExplicitRecovery(t *testing.T) {
	cfg, secret := sessionFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".counters (id int PRIMARY KEY,n counter)"})
	for _, mode := range []string{"lost", "cancel", "release", "close"} {
		t.Run(mode, func(t *testing.T) {
			id := map[string]int{"lost": 1, "cancel": 2, "release": 3, "close": 4}[mode]
			statement := "UPDATE " + ks + ".counters SET n=n+1 WHERE id=" + strconv.Itoa(id)
			proxy, applied := nativeFaultProxy(t, cfg, statement, mode != "lost")
			s := fixtureSession(t, proxy, secret, ks)
			l := fixtureLease(t, s, 21)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			type outcome struct {
				r   contract.SQLResult
				err error
			}
			done := make(chan outcome, 1)
			go func() { r, err := l.Execute(ctx, "", statement); done <- outcome{r, err} }()
			select {
			case ok := <-applied:
				if !ok {
					t.Fatal("real counter VOID missing")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("real acknowledgement missing")
			}
			switch mode {
			case "cancel":
				cancel()
			case "release":
				l.Release()
			case "close":
				s.Close()
			}
			select {
			case out := <-done:
				if !errors.Is(out.err, contract.ErrSQLSessionLost) || len(out.r.Steps) != 1 || out.r.Steps[0].State != "uncertain" {
					t.Fatal("unknown lease write boundary lost", out.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original lease worker did not acknowledge cancellation")
			}
			if _, err := l.Execute(context.Background(), "", statement); !errors.Is(err, contract.ErrSQLSessionLost) && !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("unknown command replayed")
			}
			if mode != "close" {
				if !s.Alive() {
					t.Fatal("losing a CQL lease revoked original metadata session")
				}
				replacement, err := l.Replace()
				if err != nil {
					t.Fatal(err)
				}
				defer replacement.Release()
				verification := leaseQuery(t, replacement, "", "SELECT n FROM counters WHERE id="+strconv.Itoa(id))
				if len(verification.Rows) != 1 || verification.Sets[0].Rows[0][0].Text != "1" {
					t.Fatal("replacement replayed counter mutation")
				}
			} else if _, err := l.Replace(); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("closed original session recovered implicitly")
			}
			actual := query(t, c, Request{Statement: "SELECT n FROM " + ks + ".counters WHERE id=" + strconv.Itoa(id)})
			if len(actual.Rows) != 1 || actual.Rows[0][0].Text != "1" {
				t.Fatal("counter acknowledgement lost or replayed")
			}
		})
	}
}

func TestCassandraNativeMetadataChangeQueueAndOriginalClose(t *testing.T) {
	cfg, secret := sessionFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".rows (id int PRIMARY KEY,n int)"})
	var fired atomic.Bool
	changed := make(chan error, 1)
	proxy := nativeFrameProxy(t, cfg, func(op byte, body []byte, response frame, _ net.Conn) bool {
		if op == 7 && bytes.Contains(body, []byte("SELECT * FROM system_schema.columns")) && fired.CompareAndSwap(false, true) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := c.Execute(ctx, Request{Statement: "ALTER TABLE " + ks + ".rows ADD added text"})
			changed <- err
		}
		return false
	})
	s := fixtureSession(t, proxy, secret, ks)
	if m, err := s.TableMetadata(context.Background(), s.Namespace(ks).Object("table", "rows")); !errors.Is(err, ErrMetadataChanged) || m != nil {
		t.Fatal("concurrently mixed schema published", err)
	}
	if err := <-changed; err != nil {
		t.Fatal("real schema mutation failed", err)
	}
	m, err := s.TableMetadata(context.Background(), s.Namespace(ks).Object("table", "rows"))
	if err != nil || len(m.Columns) != 3 {
		t.Fatal("explicit schema refresh failed", err)
	}
	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- s.withMetadata(context.Background(), func(ctx context.Context, _ *Client) error { close(entered); <-ctx.Done(); return ctx.Err() })
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Namespaces(ctx); !errors.Is(err, context.Canceled) || !s.client.Alive() {
		t.Fatal("queued cancellation retired original socket", err)
	}
	for i := 0; i < 3; i++ {
		s.metadataQueue <- struct{}{}
	}
	if _, err := s.Namespaces(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatal("metadata queue unbounded", err)
	}
	for i := 0; i < 3; i++ {
		<-s.metadataQueue
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("original session close did not wait/settle")
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("original metadata worker did not cancel", err)
	}
	if s.Alive() || s.client.Alive() {
		t.Fatal("closed original session remained alive")
	}
	if _, err := s.Namespaces(context.Background()); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed metadata session silently reopened")
	}
	if err := s.Close(); err != nil {
		t.Fatal("close not idempotent")
	}
}
