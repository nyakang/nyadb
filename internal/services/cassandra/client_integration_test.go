package cassandra

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeFixture(t *testing.T) (connections.CassandraConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_CASSANDRA_IT") != "1" {
		t.Skip("requires an owned Cassandra fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_CASSANDRA_CONFIG"))
	if err != nil {
		t.Fatal("private fixture config unavailable")
	}
	var cfg struct {
		Endpoint, Auth, Username, TLSMode string
		CAFile                            string `json:"ca_file"`
		ServerName                        string `json:"server_name"`
		PasswordFile                      string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private fixture config")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("private fixture credential unavailable")
	}
	return connections.CassandraConfig{Endpoint: cfg.Endpoint, Auth: cfg.Auth, Username: cfg.Username, TLSMode: cfg.TLSMode, CAFile: cfg.CAFile, ServerName: cfg.ServerName}, string(secret)
}
func fixtureClient(t *testing.T, cfg connections.CassandraConfig, secret string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatalf("native open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func query(t *testing.T, c *Client, r Request) *Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := c.Execute(ctx, r)
	if err != nil {
		t.Fatalf("native request: %v", err)
	}
	return out
}
func fixtureKeyspace(t *testing.T, c *Client, cfg connections.CassandraConfig, secret string) string {
	t.Helper()
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	ks := "nyadb_" + hex.EncodeToString(id[:])
	query(t, c, Request{Statement: "CREATE KEYSPACE " + ks + " WITH replication={'class':'SimpleStrategy','replication_factor':1}"})
	t.Cleanup(func() {
		cleanup := c
		if !c.Alive() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var err error
			cleanup, err = Open(ctx, cfg, secret)
			if err != nil {
				t.Errorf("cleanup open: %v", err)
				return
			}
			defer cleanup.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := cleanup.Execute(ctx, Request{Statement: "DROP KEYSPACE " + ks}); err != nil {
			t.Errorf("owned keyspace cleanup: %v", err)
		}
	})
	return ks
}
func integerParameter(n int32) Parameter {
	w := &writer{}
	w.i32(n)
	return Parameter{Bytes: w.Bytes()}
}

func TestCassandraNativeIdentityTypesAndParameters(t *testing.T) {
	cfg, secret := nativeFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	if c.Identity().ReleaseVersion != "5.0.6" || len(c.Identity().HostID) != 36 {
		t.Fatal("wrong native identity")
	}
	if _, err := c.Execute(context.Background(), Request{Statement: "invalid native syntax"}); err == nil || !c.Alive() {
		t.Fatal("complete syntax error lost its non-executed boundary")
	}
	change := query(t, c, Request{Statement: "CREATE TYPE " + ks + ".payload (empty text, nil_text text, absent int)"})
	if change.SchemaChange == nil || change.SchemaChange.Change != "CREATED" || change.SchemaChange.Target != "TYPE" || change.SchemaChange.Keyspace != ks || change.SchemaChange.Name != "payload" {
		t.Fatal("qualified schema change lost")
	}
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".typed (id int PRIMARY KEY, a ascii, b bigint, bl blob, bo boolean, de decimal, dou double, fl float, ip inet, si smallint, ti tinyint, tx text, ts timestamp, da date, tm time, u uuid, tu timeuuid, vi varint, du duration, li list<int>, se set<text>, ma map<int,text>, ud frozen<payload>, tp tuple<int,text,text>)"})
	query(t, c, Request{Statement: "INSERT INTO " + ks + ".typed (id,a,b,bl,bo,de,dou,fl,ip,si,ti,tx,ts,da,tm,u,tu,vi,du,li,se,ma) VALUES (1,'ASCII',9223372036854775807,0x00ff,true,123456789012345678901234567890.123456789,-1.25,0.5,'2001:db8::1',-32768,-128,'猫',0,'1970-01-01','23:59:59.999999999',123e4567-e89b-12d3-a456-426614174000,d2177dd0-eaa2-11de-a572-001b779c76e3,-123456789012345678901234567890,1mo2d3ns,[1,-2],{'猫','z'},{1:'one',-2:'two'})"})
	ud := &writer{}
	ud.blob([]byte{})
	ud.i32(-1)
	tp := &writer{}
	item := &writer{}
	item.i32(42)
	tp.blob(item.Bytes())
	tp.i32(-1)
	query(t, c, Request{Statement: "UPDATE " + ks + ".typed SET ud=?,tp=? WHERE id=?", Parameters: []Parameter{{Bytes: ud.Bytes()}, {Bytes: tp.Bytes()}, integerParameter(1)}})
	r := query(t, c, Request{Statement: "SELECT * FROM " + ks + ".typed WHERE id=?", Parameters: []Parameter{integerParameter(1)}})
	if len(r.Rows) != 1 {
		t.Fatal("missing typed row")
	}
	values := map[string]Value{}
	for i, col := range r.Columns {
		if col.Keyspace != ks || col.Table != "typed" {
			t.Fatal("unqualified metadata")
		}
		values[col.Name] = r.Rows[0][i]
	}
	for name, want := range map[string]string{"a": "ASCII", "b": "9223372036854775807", "bl": "AP8=", "bo": "true", "de": "123456789012345678901234567890.123456789", "dou": "-1.25", "fl": "0.5", "ip": "2001:db8::1", "si": "-32768", "ti": "-128", "tx": "猫", "ts": "0", "da": "0", "tm": "86399999999999", "u": "123e4567-e89b-12d3-a456-426614174000", "tu": "d2177dd0-eaa2-11de-a572-001b779c76e3", "vi": "-123456789012345678901234567890", "du": "1mo2d3ns"} {
		if values[name].Text != want {
			t.Errorf("%s got %q want %q", name, values[name].Text, want)
		}
	}
	if len(values["li"].Items) != 2 || values["li"].Items[1].Text != "-2" || len(values["se"].Items) != 2 || len(values["ma"].Entries) != 2 || values["ma"].Entries[0].Key.Type.Code != 9 {
		t.Fatal("native collections flattened")
	}
	fields := values["ud"].Fields
	if values["du"].Type.Code != 0 || values["du"].Type.Custom != "org.apache.cassandra.db.marshal.DurationType" {
		t.Fatal("native protocol v4 duration type changed")
	}
	items := values["tp"].Items
	if len(fields) != 3 || fields[0].Value.Null || fields[0].Value.Missing || len(fields[0].Value.Raw) != 0 || !fields[1].Value.Null || (!fields[2].Value.Null && !fields[2].Value.Missing) {
		t.Fatal("native UDT lost null/empty/omitted field")
	}
	if len(items) != 3 || items[0].Text != "42" || !items[1].Null || (!items[2].Null && !items[2].Missing) {
		t.Fatal("native tuple lost null/omitted field")
	}
	t.Logf("native trailing UDT field: null=%t missing=%t; tuple: null=%t missing=%t", fields[2].Value.Null, fields[2].Value.Missing, items[2].Null, items[2].Missing)
	query(t, c, Request{Statement: "UPDATE " + ks + ".typed SET tx=? WHERE id=?", Parameters: []Parameter{{Unset: true}, integerParameter(1)}})
	if query(t, c, Request{Statement: "SELECT tx FROM " + ks + ".typed WHERE id=1"}).Rows[0][0].Text != "猫" {
		t.Fatal("UNSET overwrote field")
	}
	query(t, c, Request{Statement: "UPDATE " + ks + ".typed SET tx=? WHERE id=?", Parameters: []Parameter{{Null: true}, integerParameter(1)}})
	if !query(t, c, Request{Statement: "SELECT tx FROM " + ks + ".typed WHERE id=1"}).Rows[0][0].Null {
		t.Fatal("NULL was not preserved")
	}
	query(t, c, Request{Statement: "UPDATE " + ks + ".typed SET tx=? WHERE id=?", Parameters: []Parameter{{Bytes: []byte{}}, integerParameter(1)}})
	if v := query(t, c, Request{Statement: "SELECT tx FROM " + ks + ".typed WHERE id=1"}).Rows[0][0]; v.Null || v.Text != "" {
		t.Fatal("empty string became NULL")
	}
	query(t, c, Request{Statement: "INSERT INTO " + ks + ".typed (id,tx) VALUES (2,'original') IF NOT EXISTS", SerialConsistency: 9})
	lwt := query(t, c, Request{Statement: "INSERT INTO " + ks + ".typed (id,tx) VALUES (2,'replacement') IF NOT EXISTS", SerialConsistency: 9})
	if lwt.Columns[0].Name != "[applied]" || lwt.Rows[0][0].Text != "false" {
		t.Fatal("LWT rejected condition was not preserved")
	}
	if query(t, c, Request{Statement: "USE " + ks}).Keyspace != ks {
		t.Fatal("SET_KEYSPACE not decoded")
	}
}

func TestCassandraNativePagingBoundToPhysicalRequest(t *testing.T) {
	cfg, secret := nativeFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".pages (p int,n int,v text,PRIMARY KEY(p,n))"})
	var batch strings.Builder
	batch.WriteString("BEGIN UNLOGGED BATCH ")
	for n := range 23 {
		batch.WriteString("INSERT INTO " + ks + ".pages(p,n,v) VALUES (1,")
		batch.WriteString(strconv.Itoa(n))
		batch.WriteString(",'page'); ")
	}
	batch.WriteString("APPLY BATCH")
	query(t, c, Request{Statement: batch.String()})
	request := Request{Statement: "SELECT n,v FROM " + ks + ".pages WHERE p=?", Parameters: []Parameter{integerParameter(1)}, PageSize: 4, Consistency: 10}
	page := query(t, c, request)
	if page.Next == nil {
		t.Fatal("missing native cursor")
	}
	original := page.Next.Clone()
	other := fixtureClient(t, cfg, secret)
	foreign := request
	foreign.Cursor = original
	if _, err := other.Execute(context.Background(), foreign); !errors.Is(err, ErrInvalid) {
		t.Fatal("cursor crossed physical sessions")
	}
	for _, mutate := range []func(*Request){func(r *Request) { r.Statement += " " }, func(r *Request) { r.PageSize = 5 }, func(r *Request) { r.Parameters[0] = integerParameter(2) }, func(r *Request) { r.Consistency = 1 }, func(r *Request) { r.SerialConsistency = 9 }, func(r *Request) { r.Cursor.state[0] ^= 1 }} {
		v := request
		v.Parameters = append([]Parameter(nil), request.Parameters...)
		v.Cursor = original.Clone()
		mutate(&v)
		if _, err := c.Execute(context.Background(), v); !errors.Is(err, ErrInvalid) || !c.Alive() {
			t.Fatal("changed pagination request accepted")
		}
	}
	seen := map[string]bool{}
	pages := 0
	for {
		pages++
		for _, row := range page.Rows {
			if seen[row[0].Text] {
				t.Fatal("duplicate row")
			}
			seen[row[0].Text] = true
		}
		if page.Next == nil {
			break
		}
		request.Cursor = page.Next.Clone()
		page = query(t, c, request)
		if pages > 10 {
			t.Fatal("pagination did not finish")
		}
	}
	if len(seen) != 23 || pages < 6 {
		t.Fatalf("incomplete pagination: %d rows in %d pages", len(seen), pages)
	}
	c.Close()
	if _, err := c.Execute(context.Background(), foreign); !errors.Is(err, ErrRetired) {
		t.Fatal("cursor revived original closed connection")
	}
}

func TestCassandraNativeAuthenticationAndTLS(t *testing.T) {
	cfg, secret := nativeFixture(t)
	fixtureClient(t, cfg, secret)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if cfg.Auth == "password" {
		if c, err := Open(ctx, cfg, "wrong-password"); err == nil {
			c.Close()
			t.Fatal("wrong password accepted")
		} else if !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "wrong-password") {
			t.Fatal("authentication error boundary")
		}
		none := cfg
		none.Auth = "none"
		none.Username = ""
		if c, err := Open(ctx, none, ""); err == nil {
			c.Close()
			t.Fatal("password server accepted none")
		}
		for _, mutate := range []func(*connections.CassandraConfig){func(c *connections.CassandraConfig) { c.CAFile = "" }, func(c *connections.CassandraConfig) { c.ServerName = "wrong.invalid" }, func(c *connections.CassandraConfig) { c.TLSMode = "disable"; c.CAFile = ""; c.ServerName = "" }} {
			v := cfg
			mutate(&v)
			if c, err := Open(ctx, v, secret); err == nil {
				c.Close()
				t.Fatal("TLS authentication bypassed")
			}
		}
	} else {
		v := cfg
		v.Auth = "password"
		v.Username = "native"
		if c, err := Open(ctx, v, "unused"); err == nil {
			c.Close()
			t.Fatal("none server silently ignored password auth")
		}
	}
}

func nativeFaultProxy(t *testing.T, cfg connections.CassandraConfig, match string, park bool) (connections.CassandraConfig, <-chan bool) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	seen := make(chan bool, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		front, err := listener.Accept()
		if err != nil {
			return
		}
		defer front.Close()
		var back net.Conn
		if cfg.TLSMode == "disable" {
			back, err = (&net.Dialer{}).DialContext(ctx, "tcp", cfg.Endpoint)
		} else {
			raw, e := os.ReadFile(cfg.CAFile)
			if e != nil {
				return
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(raw) {
				return
			}
			host, _, _ := net.SplitHostPort(cfg.Endpoint)
			name := cfg.ServerName
			if name == "" {
				name = host
			}
			back, err = (&tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", cfg.Endpoint)
		}
		if err != nil {
			return
		}
		defer back.Close()
		stop := context.AfterFunc(ctx, func() { front.Close(); back.Close() })
		defer stop()
		for {
			var header [9]byte
			if _, err = io.ReadFull(front, header[:]); err != nil {
				return
			}
			size := int32(binary.BigEndian.Uint32(header[5:]))
			if size < 0 || size > MaxFrame {
				return
			}
			body := make([]byte, int(size))
			if _, err = io.ReadFull(front, body); err != nil {
				return
			}
			if err = writeFrame(back, header[4], body); err != nil {
				return
			}
			response, e := readFrame(back)
			if e != nil {
				return
			}
			if header[4] == 7 && bytes.Contains(body, []byte(match)) {
				seen <- response.opcode == 8 && len(response.body) >= 4 && binary.BigEndian.Uint32(response.body[:4]) == 1
				if park {
					_, _ = io.Copy(io.Discard, front)
				}
				return
			}
			header[0] = 0x84
			header[1] = response.flags
			header[4] = response.opcode
			binary.BigEndian.PutUint32(header[5:], uint32(len(response.body)))
			if _, err = front.Write(header[:]); err != nil {
				return
			}
			if _, err = front.Write(response.body); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("original proxy worker did not close")
		}
	})
	v := cfg
	v.Endpoint = listener.Addr().String()
	v.TLSMode = "disable"
	v.CAFile = ""
	v.ServerName = ""
	return v, seen
}

func TestCassandraNativeLostCounterReplyAndCancellation(t *testing.T) {
	cfg, secret := nativeFixture(t)
	original := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, original, cfg, secret)
	query(t, original, Request{Statement: "CREATE TABLE " + ks + ".counter_values (id int PRIMARY KEY,n counter)"})
	for _, park := range []bool{false, true} {
		name := "lost"
		id := 1
		if park {
			name = "cancel"
			id = 2
		}
		t.Run(name, func(t *testing.T) {
			statement := "UPDATE " + ks + ".counter_values SET n=n+1 WHERE id=" + strconv.Itoa(id)
			proxy, applied := nativeFaultProxy(t, cfg, statement, park)
			c := fixtureClient(t, proxy, secret)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.Execute(ctx, Request{Statement: statement}); done <- err }()
			select {
			case ok := <-applied:
				if !ok {
					t.Fatal("backend did not acknowledge counter write")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("backend write acknowledgement missing")
			}
			if park {
				cancel()
			}
			select {
			case err := <-done:
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || c.Alive() {
					t.Fatalf("unknown native write boundary: %v", err)
				}
				if park && !errors.Is(err, context.Canceled) {
					t.Fatal("cancel cause lost")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original worker did not acknowledge interruption")
			}
			if _, err := c.Execute(context.Background(), Request{Statement: statement}); !errors.Is(err, ErrRetired) {
				t.Fatal("counter mutation replayed")
			}
			recovery := fixtureClient(t, cfg, secret)
			out := query(t, recovery, Request{Statement: "SELECT n FROM " + ks + ".counter_values WHERE id=" + strconv.Itoa(id)})
			if len(out.Rows) != 1 || out.Rows[0][0].Text != "1" {
				t.Fatal("counter was lost or replayed")
			}
			if recovery.Identity().HostID != original.Identity().HostID {
				t.Fatal("explicit verification changed node identity")
			}
		})
	}
}
