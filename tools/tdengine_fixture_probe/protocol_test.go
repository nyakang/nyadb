package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/taosdata/driver-go/v3/ws/unified"
)

func TestExactNativeValueEncoding(t *testing.T) {
	for _, test := range []struct {
		value any
		typ   string
		want  cell
	}{
		{int64(9007199254740993), "BIGINT", cell{Text: "9007199254740993"}},
		{uint64(math.MaxUint64), "BIGINT UNSIGNED", cell{Text: "18446744073709551615"}},
		{float32(8.6), "FLOAT", cell{Text: "8.6"}},
		{math.Inf(1), "DOUBLE", cell{Text: "+Inf"}},
		{nil, "BIGINT", cell{Null: true}},
		{"", "NCHAR", cell{}},
		{[]byte{0, 255}, "VARBINARY", cell{Binary: "AP8="}},
		{[]byte("文本 Ω"), "VARCHAR", cell{Text: "文本 Ω"}},
		{time.Unix(1704067200, 123456789), "TIMESTAMP", cell{Text: "2024-01-01T00:00:00.123456789Z", UnixNano: "1704067200123456789"}},
	} {
		got, err := encodeCell(test.value, test.typ)
		if err != nil || got != test.want {
			t.Fatalf("%T/%s: got %#v, error %v, want %#v", test.value, test.typ, got, err, test.want)
		}
	}
	if _, err := encodeCell(struct{}{}, "unknown"); err == nil {
		t.Fatal("unhandled native values must not be silently serialized")
	}
}

var fixtureSequence atomic.Uint64

func native(t *testing.T) (fixtureConfig, string, *unified.Client) {
	t.Helper()
	if os.Getenv("NYADB_TDENGINE_PROTOCOL_IT") != "1" {
		t.Skip("requires explicitly owned native fixture")
	}
	cfg, password, err := loadConfig(os.Getenv("NYADB_TDENGINE_PROTOCOL_CONFIG"))
	if err != nil {
		t.Fatal("private fixture configuration unavailable")
	}
	if !regexp.MustCompile(`^nyadb_td_[a-f0-9]{16}$`).MatchString(cfg.DatabasePrefix) || cfg.Version != "3.3.6.13" {
		t.Fatal("unexpected fixture ownership or native version")
	}
	c, err := connect(cfg, password)
	if err != nil {
		t.Fatal(strings.ReplaceAll(err.Error(), password, "[redacted]"))
	}
	t.Cleanup(c.Close)
	return cfg, password, c
}

func exec(t *testing.T, c *unified.Client, sql string) int {
	t.Helper()
	n, err := c.Exec(0, sql)
	if err != nil {
		t.Fatalf("native SQL %q: %v", sql, err)
	}
	return n
}

func read(t *testing.T, c *unified.Client, sql string) result {
	t.Helper()
	r, err := query(c, sql)
	if err != nil {
		t.Fatalf("native query %q: %v", sql, err)
	}
	return r
}

func evidence(t *testing.T, value any) {
	t.Helper()
	dir := os.Getenv("NYADB_TDENGINE_PROTOCOL_EVIDENCE")
	if dir == "" {
		t.Fatal("native evidence directory missing")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, t.Name()+".json"), append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func database(t *testing.T, cfg fixtureConfig, c *unified.Client) string {
	t.Helper()
	db := fmt.Sprintf("%s_%d", cfg.DatabasePrefix, fixtureSequence.Add(1))
	exec(t, c, fmt.Sprintf("CREATE DATABASE `%s` PRECISION 'ns' VGROUPS 1", db))
	t.Cleanup(func() {
		if _, err := c.Exec(0, "DROP DATABASE `"+db+"`"); err != nil {
			t.Errorf("owned database cleanup failed: %v", err)
		}
	})
	exec(t, c, "USE `"+db+"`")
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := read(t, c, "SHOW VGROUPS")
		ready := len(r.Rows) > 0
		index := -1
		boolean := false
		for i, column := range r.Columns {
			if column.Name == "is_ready" {
				index = i
				boolean = true
				break
			}
			if column.Name == "v1_status" {
				index = i
			}
		}
		if index < 0 {
			t.Fatalf("native fixture lacks recognized VGroup readiness: %#v", r.Columns)
		}
		for _, row := range r.Rows {
			if boolean {
				ready = ready && row[index].Text == "true"
			} else {
				ready = ready && row[index].Text == "leader"
			}
		}
		if ready {
			return db
		}
		if time.Now().After(deadline) {
			t.Fatal("owned database VGroups did not become ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestNativeTDengineVersionAuthenticationAndPhysicalIsolation(t *testing.T) {
	cfg, password, c := native(t)
	r := read(t, c, "SELECT server_version(), timezone()")
	if len(r.Rows) != 1 || r.Rows[0][0].Text != cfg.Version {
		t.Fatal("pinned native version mismatch")
	}
	bad, err := connect(cfg, password+"-wrong")
	if bad != nil {
		bad.Close()
	}
	if err == nil || strings.Contains(err.Error(), password) {
		t.Fatal("wrong native credentials were accepted or exposed")
	}
	db := database(t, cfg, c)
	different, err := connect(cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer different.Close()
	selected := read(t, c, "SELECT database()")
	unselected := read(t, different, "SELECT database()")
	if selected.Rows[0][0].Text != db || !unselected.Rows[0][0].Null {
		t.Fatal("native database selection leaked to a distinct physical session")
	}
	evidence(t, map[string]any{"version_timezone": r, "selected": selected, "separate_physical_session": unselected, "wrong_credentials_rejected": true})
}

func TestNativeTDengineTablesStablesChildrenAndCompleteSources(t *testing.T) {
	cfg, _, c := native(t)
	db := database(t, cfg, c)
	exec(t, c, "CREATE STABLE meters (ts TIMESTAMP, `value` INT, note NCHAR(64)) TAGS (location NCHAR(64), group_id INT)")
	exec(t, c, "CREATE TABLE child_a USING meters TAGS ('甲 Ω', 1)")
	exec(t, c, "CREATE TABLE child_b USING meters TAGS ('乙 Ω', 2)")
	exec(t, c, "CREATE TABLE ordinary (ts TIMESTAMP, `value` INT)")
	exec(t, c, "INSERT INTO child_a VALUES (1704067200123456789, 11, 'a') child_b VALUES (1704067200123456789, 22, 'b')")
	stables := read(t, c, "SHOW STABLES")
	tables := read(t, c, "SELECT table_name, stable_name FROM information_schema.ins_tables WHERE db_name = '"+db+"' ORDER BY table_name")
	columns := read(t, c, "DESCRIBE meters")
	source := read(t, c, "SHOW CREATE STABLE meters")
	childSource := read(t, c, "SHOW CREATE TABLE child_a")
	rows := read(t, c, "SELECT tbname, ts, `value`, location FROM meters ORDER BY tbname")
	if len(stables.Rows) != 1 || len(tables.Rows) != 3 || len(rows.Rows) != 2 || rows.Rows[0][1].UnixNano != rows.Rows[1][1].UnixNano {
		t.Fatal("native table hierarchy or supertable multi-child timestamp identity lost")
	}
	if tables.Rows[0][1].Text != "meters" || tables.Rows[1][1].Text != "meters" || !tables.Rows[2][1].Null {
		t.Fatal("native stable parent relationships lost")
	}
	if len(columns.Rows) != 5 || columns.Rows[0][1].Text != "TIMESTAMP" || columns.Rows[3][3].Text != "TAG" {
		t.Fatal("native timestamp/data/TAG metadata mismatch")
	}
	if !strings.Contains(source.Rows[0][1].Text, "TAGS") || !strings.Contains(childSource.Rows[0][1].Text, "USING") {
		t.Fatal("original stable/child source missing")
	}
	evidence(t, map[string]any{"stables": stables, "tables": tables, "columns": columns, "native_ddl": source, "child_ddl": childSource, "stable_rows": rows, "stable_timestamp_alone_is_not_unique": true})
}

func TestNativeTDengineExactTypesNullTextBinaryAndTimestamp(t *testing.T) {
	cfg, _, c := native(t)
	database(t, cfg, c)
	exec(t, c, "CREATE TABLE exact_values (ts TIMESTAMP, signed BIGINT, unsigned_value BIGINT UNSIGNED, f FLOAT, d DOUBLE, active BOOL, note NCHAR(64), txt VARCHAR(64), payload VARBINARY(64), missing INT)")
	n := exec(t, c, "INSERT INTO exact_values VALUES (1704067200123456789, 9007199254740993, 18446744073709551614, 8.6, 8.600000381469727, true, '文本 Ω', '', '\\x00ff', NULL)")
	r := read(t, c, "SELECT * FROM exact_values")
	if n != 1 || len(r.Rows) != 1 {
		t.Fatal("original native insert acknowledgement missing")
	}
	row := r.Rows[0]
	if row[0].UnixNano != "1704067200123456789" || row[1].Text != "9007199254740993" || row[2].Text != "18446744073709551614" || row[3].Text != "8.6" || row[4].Text != "8.600000381469727" || row[5].Text != "true" || row[6].Text != "文本 Ω" || row[7].Null || row[7].Text != "" || row[8].Binary != "AP8=" || !row[9].Null {
		t.Fatalf("native exact typed value mismatch: %#v", row)
	}
	empty := read(t, c, "SELECT * FROM exact_values WHERE signed=1")
	if len(empty.Rows) != 0 || len(empty.Columns) != 10 {
		t.Fatal("empty native result discarded original columns")
	}
	evidence(t, map[string]any{"insert_affected_rows": n, "complete_native_values": r, "empty_result": empty})
}

func TestNativeTDengineOriginalCursorPagingAndClose(t *testing.T) {
	cfg, _, c := native(t)
	database(t, cfg, c)
	exec(t, c, "CREATE TABLE `pages` (ts TIMESTAMP, id INT)")
	var sql strings.Builder
	sql.WriteString("INSERT INTO `pages` VALUES ")
	for i := 0; i < 1051; i++ {
		fmt.Fprintf(&sql, "(%d,%d) ", int64(1704067200000000000)+int64(i), i)
	}
	if exec(t, c, sql.String()) != 1051 {
		t.Fatal("complete original insert count mismatch")
	}
	page1 := read(t, c, "SELECT * FROM `pages` ORDER BY ts LIMIT 512 OFFSET 0")
	page2 := read(t, c, "SELECT * FROM `pages` ORDER BY ts LIMIT 512 OFFSET 512")
	page3 := read(t, c, "SELECT * FROM `pages` ORDER BY ts LIMIT 512 OFFSET 1024")
	full := read(t, c, "SELECT * FROM `pages` ORDER BY ts")
	if len(page1.Rows) != 512 || len(page2.Rows) != 512 || len(page3.Rows) != 27 || len(full.Rows) != 1051 || page2.Rows[0][1].Text != "512" || page3.Rows[26][1].Text != "1050" {
		t.Fatal("native pages or complete raw-block stream mismatch")
	}
	r, err := c.Query(0, "SELECT * FROM `pages`")
	if err != nil || r == nil {
		t.Fatal("native cursor unavailable")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if len(read(t, c, "SELECT count(*) FROM `pages`").Rows) != 1 {
		t.Fatal("closing original result broke its physical session")
	}
	evidence(t, map[string]any{"page_lengths": []int{len(page1.Rows), len(page2.Rows), len(page3.Rows)}, "complete_stream": full, "closed_original_cursor": true})
}

func TestNativeTDengineInsertUpdateModesAndUnsupportedUpdate(t *testing.T) {
	cfg, _, c := native(t)
	observed := map[string]any{}
	// UPDATE 0/1/2 belongs to older server generations. The actual v3
	// parser rejects it; do not silently project those modes onto v3.
	for _, mode := range []int{0, 1, 2} {
		db := fmt.Sprintf("%s_rejected_%d", cfg.DatabasePrefix, mode)
		_, err := c.Exec(0, fmt.Sprintf("CREATE DATABASE `%s` UPDATE %d", db, mode))
		if err == nil {
			_, _ = c.Exec(0, "DROP DATABASE `"+db+"`")
			t.Fatal("legacy database UPDATE option unexpectedly accepted by v3")
		}
		observed[fmt.Sprint(mode)] = err.Error()
	}
	database(t, cfg, c)
	exec(t, c, "CREATE TABLE edits (ts TIMESTAMP, a INT, b INT)")
	first := exec(t, c, "INSERT INTO edits VALUES (1704067200123456789, 1, 2)")
	replacement := exec(t, c, "INSERT INTO edits (ts,a) VALUES (1704067200123456789, 9)")
	r := read(t, c, "SELECT * FROM edits")
	if len(r.Rows) != 1 || first != 1 || replacement != 1 || r.Rows[0][1].Text != "9" || r.Rows[0][2].Text != "2" {
		t.Fatalf("native v3 partial INSERT behavior mismatch: acknowledgements=%d/%d rows=%#v", first, replacement, r.Rows)
	}
	_, err := c.Exec(0, "UPDATE edits SET a=33 WHERE ts=1704067200123456789")
	if err == nil {
		t.Fatal("ordinary SQL UPDATE unexpectedly accepted")
	}
	evidence(t, map[string]any{"rejected_legacy_update_modes": observed, "first_ack": first, "replacement_ack": replacement, "after": r, "ordinary_update_error": err.Error()})
}

func TestNativeTDengineCompositeKeysAndDeleteScope(t *testing.T) {
	cfg, _, c := native(t)
	database(t, cfg, c)
	exec(t, c, "CREATE TABLE keyed (ts TIMESTAMP, id BIGINT PRIMARY KEY, `value` INT)")
	exec(t, c, "INSERT INTO keyed VALUES (1704067200123456789,1,11) (1704067200123456789,2,22)")
	columns := read(t, c, "DESCRIBE keyed")
	before := read(t, c, "SELECT * FROM keyed ORDER BY id")
	_, badDelete := c.Exec(0, "DELETE FROM keyed WHERE ts=1704067200123456789 AND id=1")
	if badDelete == nil {
		t.Fatal("composite secondary-key DELETE unexpectedly accepted")
	}
	retained := read(t, c, "SELECT * FROM keyed ORDER BY id")
	if len(before.Rows) != 2 || len(retained.Rows) != 2 || !strings.Contains(columns.Rows[1][3].Text, "COMPOSITE KEY") {
		t.Fatal("original composite identity or rejected-delete rows lost")
	}
	deleted := exec(t, c, "DELETE FROM keyed WHERE ts=1704067200123456789")
	after := read(t, c, "SELECT * FROM keyed")
	if len(after.Rows) != 0 {
		t.Fatal("timestamp DELETE did not remove the complete timestamp group")
	}
	evidence(t, map[string]any{"columns": columns, "before": before, "rejected_secondary_key_delete": badDelete.Error(), "retained": retained, "timestamp_delete_ack": deleted, "after": after})
}

func TestNativeTDengineSequentialBatchIsNotAtomic(t *testing.T) {
	cfg, _, c := native(t)
	database(t, cfg, c)
	exec(t, c, "CREATE TABLE receipts (ts TIMESTAMP, `value` INT)")
	errors := map[string]string{}
	for _, sql := range []string{"BEGIN", "COMMIT", "ROLLBACK"} {
		_, err := c.Exec(0, sql)
		if err == nil {
			t.Fatalf("native %s unexpectedly accepted as a transaction", sql)
		}
		errors[sql] = err.Error()
	}
	first := exec(t, c, "INSERT INTO receipts VALUES (1704067200123456789,1)")
	_, failed := c.Exec(0, "INSERT INTO missing_original_table VALUES (1704067200123456790,2)")
	if failed == nil {
		t.Fatal("intentional later-statement failure missing")
	}
	r := read(t, c, "SELECT * FROM receipts")
	if first != 1 || len(r.Rows) != 1 || r.Rows[0][1].Text != "1" {
		t.Fatal("successful prefix of non-atomic statements was lost")
	}
	evidence(t, map[string]any{"transaction_errors": errors, "first_statement_affected_rows": first, "later_statement_error": failed.Error(), "successful_prefix_retained": r})
}

// holdProxy captures the original server query acknowledgement and drops only
// that response. It never stores handshake messages or credentials.
type holdProxy struct {
	server   *httptest.Server
	seen     chan []byte
	stop     chan struct{}
	done     chan struct{}
	requests atomic.Uint64
	accepted atomic.Uint64
	mu       sync.Mutex
	conns    []*websocket.Conn
	once     sync.Once
}

func newHoldProxy(t *testing.T, endpoint, sql string) *holdProxy {
	t.Helper()
	p := &holdProxy{seen: make(chan []byte, 1), stop: make(chan struct{}), done: make(chan struct{})}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.accepted.Add(1)
		up, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
		if err != nil {
			http.Error(w, "owned upstream unavailable", 502)
			return
		}
		down, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			up.Close()
			return
		}
		p.mu.Lock()
		p.conns = append(p.conns, up, down)
		p.mu.Unlock()
		defer up.Close()
		defer down.Close()
		var armed atomic.Bool
		forwardDone := make(chan struct{})
		go func() {
			defer close(forwardDone)
			defer up.Close()
			for {
				kind, data, e := down.ReadMessage()
				if e != nil {
					return
				}
				if bytes.Contains(data, []byte(sql)) {
					p.requests.Add(1)
					armed.Store(true)
				}
				if e = up.WriteMessage(kind, data); e != nil {
					return
				}
			}
		}()
		defer func() { down.Close(); up.Close(); <-forwardDone }()
		for {
			kind, data, e := up.ReadMessage()
			if e != nil {
				return
			}
			var acknowledgement struct {
				IsUpdate bool `json:"is_update"`
			}
			// Binary-query transport names differ from legacy text query.
			// Capture the actual write response by its native receipt fields.
			if armed.Load() && kind == websocket.TextMessage && json.Unmarshal(data, &acknowledgement) == nil && acknowledgement.IsUpdate {
				p.seen <- append([]byte(nil), data...)
				<-p.stop
				close(p.done)
				return
			}
			if e = down.WriteMessage(kind, data); e != nil {
				return
			}
		}
	}))
	t.Cleanup(func() {
		p.once.Do(func() { close(p.stop) })
		p.mu.Lock()
		for _, c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
		p.server.Close()
	})
	return p
}

func TestNativeTDengineLostOriginalWriteAcknowledgementDoesNotReconnect(t *testing.T) {
	cfg, password, verifier := native(t)
	db := database(t, cfg, verifier)
	exec(t, verifier, "CREATE TABLE original_receipts (ts TIMESTAMP, `value` INT)")
	sql := "INSERT INTO `" + db + "`.original_receipts VALUES (now,17)"
	proxy := newHoldProxy(t, cfg.Endpoint, sql)
	originalCfg := cfg
	originalCfg.Endpoint = "ws" + strings.TrimPrefix(proxy.server.URL, "http") + "/ws"
	original, err := connect(originalCfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	finished := make(chan error, 1)
	go func() { _, e := original.Exec(0, sql); finished <- e }()
	var receipt []byte
	select {
	case receipt = <-proxy.seen:
	case err = <-finished:
		t.Fatalf("original request finished before held receipt: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("original server acknowledgement was not captured")
	}
	var raw map[string]any
	if err = json.Unmarshal(receipt, &raw); err != nil || raw["code"] != float64(0) || raw["affected_rows"] != float64(1) {
		t.Fatalf("invalid original write acknowledgement: %s", receipt)
	}
	observed := read(t, verifier, "SELECT * FROM original_receipts")
	if len(observed.Rows) != 1 || observed.Rows[0][1].Text != "17" {
		t.Fatal("server write not independently observed before lost receipt")
	}
	proxy.once.Do(func() { close(proxy.stop) })
	select {
	case err = <-finished:
		if err == nil {
			t.Fatal("lost original acknowledgement must not report success")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("original executor did not acknowledge disconnect")
	}
	_, retryError := original.Exec(0, sql)
	if retryError == nil || proxy.requests.Load() != 1 || proxy.accepted.Load() != 1 {
		t.Fatal("original physical client silently reconnected or replayed uncertain SQL")
	}
	after := read(t, verifier, "SELECT * FROM original_receipts")
	if len(after.Rows) != 1 {
		t.Fatal("attempted uncertain replay changed original server rows")
	}
	evidence(t, map[string]any{"original_server_acknowledgement": json.RawMessage(receipt), "observed_before_lost_receipt": observed, "original_executor_error": err.Error(), "retired_client_retry_error": retryError.Error(), "native_query_dispatches": proxy.requests.Load(), "original_physical_connections": proxy.accepted.Load(), "after_attempted_replay": after})
}
