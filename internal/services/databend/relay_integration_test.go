package databend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

type localTransportFixture struct {
	Endpoint, Username, PasswordFile, Package, Directory, Database string
}

func readLocalTransportFixture(t *testing.T) localTransportFixture {
	t.Helper()
	if os.Getenv("NYADB_DATABEND_TRANSPORT_LOCAL_IT") != "1" {
		t.Skip("requires an owned real Databend and separately verified original JDBC fixture")
	}
	var f localTransportFixture
	raw, err := os.ReadFile(os.Getenv("NYADB_DATABEND_TRANSPORT_FIXTURE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("private original transport fixture missing", err)
	}
	u, err := url.Parse(f.Endpoint)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || !filepath.IsAbs(f.Package) || !filepath.IsAbs(f.Directory) || !strings.HasPrefix(f.Database, "nyadb_transport_") {
		t.Fatal("owned original fixture identity missing")
	}
	return f
}

func localTransportSecret(t *testing.T, f localTransportFixture) string {
	t.Helper()
	raw, err := os.ReadFile(f.PasswordFile)
	if err != nil || len(raw) == 0 {
		t.Fatal("private original credential missing")
	}
	return string(raw)
}

func localOriginalClient(t *testing.T, f localTransportFixture) *agentbridge.Client {
	t.Helper()
	var m driverstore.Manifest
	raw, err := os.ReadFile(filepath.Join(f.Package, "manifest.json"))
	if err != nil || json.Unmarshal(raw, &m) != nil || m.Key != "databend" || m.MainClass != "com.dbx.agent.databend.DatabendAgent" || m.Runtime != "java" {
		t.Fatal("unmodified original process manifest missing", err)
	}
	spec := agentbridge.LaunchSpec{Executable: filepath.Join(f.Package, m.Executable)}
	for _, file := range m.Files {
		if filepath.IsAbs(file.Path) || strings.HasPrefix(filepath.Clean(file.Path), "..") {
			t.Fatal("original artifact escaped fixture")
		}
		path := filepath.Join(f.Package, filepath.FromSlash(file.Path))
		raw, err := os.ReadFile(path)
		hash := sha256.Sum256(raw)
		if err != nil || hex.EncodeToString(hash[:]) != file.SHA256 {
			t.Fatal("original process artifact changed", file.Path)
		}
		spec.Artifacts = append(spec.Artifacts, agentbridge.Artifact{Path: path, SHA256: file.SHA256})
	}
	classpath := make([]string, len(m.Classpath))
	for i, path := range m.Classpath {
		classpath[i] = filepath.Join(f.Package, path)
	}
	spec.Args = []string{"-cp", strings.Join(classpath, string(os.PathListSeparator)), m.MainClass}
	spec.Started = func(pid int) error {
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil || exe != spec.Executable {
			return errors.New("original Java executable identity changed")
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 20 {
			return errors.New("original process start identity missing")
		}
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "executable": exe})
		_, err = journal.Write(append(record, '\n'))
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := agentbridge.Start(ctx, spec, agentbridge.Options{RetainOriginalResponses: true, CancelWait: 5 * time.Second, ErrorCodes: agentbridge.DatabendCodes})
	if err != nil {
		t.Fatal("unmodified original process start", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error("original process close", err)
		}
	})
	recordTransportReceipt(t, f, "handshake", client.HandshakeReceipt())
	return client
}

func localOriginalParams(r *Relay, endpoint, database string) map[string]any {
	params, _ := r.ConnectionParams(database)
	// A diagnostic fault peer changes only the local authority and preserves
	// every original JDBC URL property and stdin credential.
	u, _ := url.Parse(strings.TrimPrefix(params["connection_string"].(string), "jdbc:"))
	peer, _ := url.Parse(endpoint)
	u.Host = peer.Host
	params["connection_string"] = "jdbc:" + u.String()
	return params
}

func localOriginalSession(t *testing.T, f localTransportFixture, c *agentbridge.Client, r *Relay, endpoint string) *agentbridge.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, receipt, err := c.OpenSession(ctx, localOriginalParams(r, endpoint, f.Database))
	recordTransportReceipt(t, f, "open", receipt)
	if err != nil || !receipt.ExecutorAcknowledged || !s.Alive() {
		t.Fatal("real original JDBC session open", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func localOriginalCall(t *testing.T, f localTransportFixture, s *agentbridge.Session, method string, params map[string]any) agentbridge.Receipt {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	r, err := s.Call(ctx, method, params)
	recordTransportReceipt(t, f, method, r)
	if err != nil || !r.ExecutorAcknowledged || !json.Valid(r.OriginalResponse()) {
		t.Fatal("original executor receipt missing", method, err)
	}
	return r
}

func localOriginalQuery(t *testing.T, f localTransportFixture, s *agentbridge.Session, source string) agentbridge.QuerySnapshot {
	t.Helper()
	r := localOriginalCall(t, f, s, "execute_query", map[string]any{"sql": source, "schema": f.Database, "maxRows": 1000})
	q, err := agentbridge.DecodeQuery(r, DecodeJDBCValue)
	if err != nil {
		t.Fatal("original Databend value projection", err, string(r.Result))
	}
	return q
}

func recordTransportReceipt(t *testing.T, f localTransportFixture, method string, receipt agentbridge.Receipt) {
	t.Helper()
	raw := receipt.OriginalResponse()
	hash := sha256.Sum256(raw)
	record, _ := json.Marshal(map[string]any{"test": t.Name(), "method": method, "receipt": receipt, "native_bytes": []byte(raw), "native_sha256": hex.EncodeToString(hash[:])})
	journal, err := os.OpenFile(filepath.Join(f.Directory, "original-receipts.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := journal.Write(append(record, '\n')); err != nil {
		t.Fatal(err)
	}
}

func localNativeQuery(t *testing.T, f localTransportFixture, source string) [][]string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"sql": source, "pagination": map[string]any{"wait_time_secs": 1, "max_rows_per_page": 1000}})
	req, _ := http.NewRequest("POST", f.Endpoint+"/v1/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(f.Username, localTransportSecret(t, f))
	client := &http.Client{Timeout: 20 * time.Second}
	var rows [][]string
	for i := 0; i < 100; i++ {
		reply, err := client.Do(req)
		if err != nil {
			t.Fatal("independent real Databend query", err)
		}
		raw, err := io.ReadAll(reply.Body)
		reply.Body.Close()
		var value struct {
			Data    [][]string
			Error   json.RawMessage
			NextURI string `json:"next_uri"`
		}
		if err != nil || reply.StatusCode != 200 || json.Unmarshal(raw, &value) != nil || len(value.Error) != 0 && !bytes.Equal(value.Error, []byte("null")) {
			t.Fatal("independent original HTTP query failed")
		}
		rows = append(rows, value.Data...)
		if value.NextURI == "" {
			return rows
		}
		if !strings.HasPrefix(value.NextURI, "/v1/query/") {
			t.Fatal("independent native query escaped original identity")
		}
		req, _ = http.NewRequest("GET", f.Endpoint+value.NextURI, nil)
		req.SetBasicAuth(f.Username, localTransportSecret(t, f))
	}
	t.Fatal("independent real Databend query did not terminate")
	return nil
}

func fixtureRelay(t *testing.T, f localTransportFixture, target string) *Relay {
	t.Helper()
	r, err := StartRelay(context.Background(), connections.DatabendConfig{Endpoint: target, Username: f.Username}, localTransportSecret(t, f))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func TestDatabendTransportLocalOriginalValuesCursorAndNativeStructure(t *testing.T) {
	f := readLocalTransportFixture(t)
	r := fixtureRelay(t, f, f.Endpoint)
	c := localOriginalClient(t, f)
	s := localOriginalSession(t, f, c, r, r.Endpoint())
	q := localOriginalQuery(t, f, s, "SELECT id,exact,payload,absent,txt,items,doc FROM original_values")
	row := q.SQL.Sets[0].Rows[0]
	if row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Text != "9007199254740993.12345678901234567890" ||
		row[2].Kind != contract.Binary || !bytes.Equal(row[2].Bytes, []byte{0, 255, 128}) || row[3].Kind != contract.Null || row[4].Text != "original text" ||
		row[5].Kind != contract.JSON || row[5].Text != "[9007199254740993]" || row[6].Kind != contract.JSON || row[6].Text != "{\"id\":9007199254740993}" {
		t.Fatal("original native precision/bytes/NULL/JSON changed")
	}
	if !bytes.Contains(q.Native, []byte("9007199254740993.12345678901234567890")) {
		t.Fatal("raw exact decimal receipt changed")
	}
	cursor, page, err := s.OpenQueryCursor(context.Background(), agentbridge.QueryCursorOptions{SQL: "SELECT id,payload FROM pages ORDER BY id", Schema: f.Database, PageSize: 100, MaxRows: 1000, Decoder: DecodeJDBCValue})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cursor.Close() })
	var sizes []int
	var ordinal int
	for {
		sizes = append(sizes, len(page.SQL.Sets[0].Rows))
		recordTransportReceipt(t, f, "cursor", page.Receipt)
		for _, row := range page.SQL.Sets[0].Rows {
			if row[0].Text != fmt.Sprint(ordinal) || !bytes.Equal(row[1].Bytes, []byte{0, 255}) {
				t.Fatal("original cursor repeated/reordered data", ordinal)
			}
			ordinal++
		}
		if !page.HasMore {
			break
		}
		page, err = cursor.Fetch(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
	}
	if ordinal != 205 || fmt.Sprint(sizes) != "[100 100 5]" {
		t.Fatal("original cursor bounds changed", sizes)
	}
	nativeDDL := localOriginalQuery(t, f, s, "SHOW CREATE TABLE original_values")
	if !strings.Contains(nativeDDL.SQL.Sets[0].Rows[0][1].Text, "FUSE") {
		t.Fatal("full native definition lost engine/options")
	}
	columns := localOriginalQuery(t, f, s, "DESCRIBE original_values")
	if len(columns.SQL.Sets[0].Rows) != 7 || columns.SQL.Sets[0].Rows[0][1].Text != "BIGINT UNSIGNED" || columns.SQL.Sets[0].Rows[0][2].Text != "NO" {
		t.Fatal("native structure lost original type")
	}
	originalDDL := localOriginalCall(t, f, s, "get_table_ddl", map[string]any{"schema": f.Database, "table": "original_values"})
	if bytes.Contains(originalDDL.Result, []byte("ENGINE=FUSE")) {
		t.Fatal("expected original JDBC reconstruction evidence changed; re-audit metadata")
	}
}

// forwardFixture carries real product bytes; it does not emulate Databend.
func forwardFixture(t *testing.T, endpoint string, req *http.Request) (int, http.Header, []byte) {
	t.Helper()
	u, _ := url.Parse(endpoint)
	u.Path = req.URL.Path
	u.RawQuery = req.URL.RawQuery
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Error(err)
		return 500, nil, nil
	}
	upstream, _ := http.NewRequest(req.Method, u.String(), bytes.NewReader(body))
	upstream.Header = req.Header.Clone()
	upstream.GetBody = nil
	response, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(upstream)
	if err != nil {
		t.Error("owned product forwarding failed")
		return 500, nil, nil
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Error(err)
	}
	return response.StatusCode, response.Header.Clone(), raw
}

func fixtureResponse(w http.ResponseWriter, code int, headers http.Header, body []byte) {
	for k, v := range headers {
		w.Header()[k] = v
	}
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func TestDatabendTransportLocalOriginalDriverRetriesOnlyCachedReceipt(t *testing.T) {
	f := readLocalTransportFixture(t)
	source := "INSERT INTO lost_local VALUES (17)"
	var upstreamCalls, localCalls atomic.Int32
	var idsMu sync.Mutex
	var ids []string
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		if bytes.Contains(raw, []byte(source)) {
			upstreamCalls.Add(1)
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	r := fixtureRelay(t, f, product.URL)
	loss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		match := bytes.Contains(raw, []byte(source))
		if match {
			idsMu.Lock()
			ids = append(ids, req.Header.Get(queryIDHeader))
			idsMu.Unlock()
		}
		code, headers, body := forwardFixture(t, r.Endpoint(), req)
		if match && localCalls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer loss.Close()
	c := localOriginalClient(t, f)
	s := localOriginalSession(t, f, c, r, loss.URL)
	localOriginalQuery(t, f, s, source)
	rows := localNativeQuery(t, f, "SELECT count() FROM \""+f.Database+"\".lost_local WHERE id=17")
	if len(rows) != 1 || rows[0][0] != "1" || upstreamCalls.Load() != 1 || localCalls.Load() < 2 {
		t.Fatal("original JDBC retry re-executed committed SQL", upstreamCalls.Load(), localCalls.Load())
	}
	idsMu.Lock()
	defer idsMu.Unlock()
	for _, id := range ids {
		if !validQueryID(id) || id != ids[0] {
			t.Fatal("actual initial JDBC ID/retry identity changed")
		}
	}
	status, ok := r.QueryStatus(ids[0])
	if !ok || !status.Complete || !status.Cached || status.Uncertain {
		t.Fatal("original durable cached receipt missing", status)
	}
}

func TestDatabendTransportLocalCommittedUpstreamReplyLossNeverReplays(t *testing.T) {
	f := readLocalTransportFixture(t)
	source := "INSERT INTO lost_upstream VALUES (23)"
	var calls atomic.Int32
	var originalID atomic.Value
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		match := bytes.Contains(raw, []byte(source))
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if match {
			calls.Add(1)
			originalID.Store(req.Header.Get(queryIDHeader))
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	r := fixtureRelay(t, f, product.URL)
	c := localOriginalClient(t, f)
	s := localOriginalSession(t, f, c, r, r.Endpoint())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	receipt, err := s.Call(ctx, "execute_query", map[string]any{"sql": source, "schema": f.Database, "maxRows": 10})
	recordTransportReceipt(t, f, "lost-write", receipt)
	if err == nil || !receipt.ExecutorAcknowledged || receipt.RemoteError == nil || receipt.RemoteError.OperationOutcome != "unknown" {
		t.Fatal("lost committed reply became success or rollback", err)
	}
	id, _ := originalID.Load().(string)
	status, ok := r.QueryStatus(id)
	if calls.Load() != 1 || !ok || !status.Uncertain || status.Complete {
		t.Fatal("uncertain upstream write replayed", calls.Load(), status)
	}
	if receipt.RemoteError.SQLState != "" || len(receipt.RemoteError.NativeQueryIDPrefix) != 16 || !strings.HasPrefix(id, receipt.RemoteError.NativeQueryIDPrefix) {
		t.Fatal("native original query ID prefix became SQLSTATE or another request identity")
	}
	rows := localNativeQuery(t, f, "SELECT count() FROM \""+f.Database+"\".lost_upstream WHERE id=23")
	if len(rows) != 1 || rows[0][0] != "1" {
		t.Fatal("lost original write was not actually committed")
	}
	// Recovery uses a new original process and reads only; it never retries the
	// failed immutable write. Product/workspace recovery remains a later gate.
	recoveryRelay := fixtureRelay(t, f, f.Endpoint)
	recovery := localOriginalClient(t, f)
	reader := localOriginalSession(t, f, recovery, recoveryRelay, recoveryRelay.Endpoint())
	q := localOriginalQuery(t, f, reader, "SELECT count() FROM lost_upstream WHERE id=23")
	if q.SQL.Sets[0].Rows[0][0].Text != "1" || calls.Load() != 1 {
		t.Fatal("explicit diagnostic recovery replayed the original write")
	}
}

func TestDatabendTransportLocalOriginalTLSGatewayAndAuthentication(t *testing.T) {
	f := readLocalTransportFixture(t)
	var gatewayCalls atomic.Int32
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gatewayCalls.Add(1)
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		fixtureResponse(w, code, headers, body)
	}))
	defer gateway.Close()
	ca := filepath.Join(t.TempDir(), "original-gateway.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gateway.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := StartRelay(context.Background(), connections.DatabendConfig{Endpoint: gateway.URL, Username: f.Username, CAFile: ca}, localTransportSecret(t, f))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	c := localOriginalClient(t, f)
	s := localOriginalSession(t, f, c, r, r.Endpoint())
	q := localOriginalQuery(t, f, s, "SELECT current_user()")
	if !strings.Contains(q.SQL.Sets[0].Rows[0][0].Text, f.Username) {
		t.Fatal("local credential replaced original product identity")
	}
	for _, v := range []struct{ name, ca, serverName, secret string }{
		{"untrusted-certificate", "", "", localTransportSecret(t, f)},
		{"wrong-hostname", ca, "wrong.invalid", localTransportSecret(t, f)},
		{"wrong-password", ca, "", "incorrect"},
	} {
		t.Run(v.name, func(t *testing.T) {
			bad, err := StartRelay(context.Background(), connections.DatabendConfig{Endpoint: gateway.URL, Username: f.Username, CAFile: v.ca, ServerName: v.serverName}, v.secret)
			if err != nil {
				t.Fatal(err)
			}
			defer bad.Close()
			req := relayRequest(t, bad, "POST", "/v1/query", firstQueryID, []byte("{\"sql\":\"SELECT 1\"}"))
			code, _ := relayReply(t, req)
			if code == 200 {
				t.Fatal("invalid certificate/identity reached authenticated product")
			}
			if v.name != "wrong-password" {
				before := gatewayCalls.Load()
				relayReply(t, relayRequest(t, bad, "POST", "/v1/query", secondQueryID, []byte("{\"sql\":\"SELECT 1\"}")))
				if gatewayCalls.Load() != before {
					t.Fatal("invalid trust reached gateway")
				}
			}
		})
	}
}

func TestDatabendTransportLocalOriginalQueuedCancellationAndIndependentSession(t *testing.T) {
	f := readLocalTransportFixture(t)
	started, finish := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		if bytes.Contains(raw, []byte("SELECT 424242")) && blocked.CompareAndSwap(false, true) {
			close(started)
			<-finish
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	r := fixtureRelay(t, f, product.URL)
	c := localOriginalClient(t, f)
	first := localOriginalSession(t, f, c, r, r.Endpoint())
	second := localOriginalSession(t, f, c, r, r.Endpoint())
	type outcome struct {
		receipt agentbridge.Receipt
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		receipt, err := first.Call(context.Background(), "execute_query", map[string]any{"sql": "SELECT 424242", "schema": f.Database, "maxRows": 10})
		done <- outcome{receipt, err}
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queued, err := first.Call(ctx, "execute_query", map[string]any{"sql": "SELECT 999999", "schema": f.Database, "maxRows": 10})
	if !errors.Is(err, context.Canceled) || queued.Dispatched || queued.CancellationRequested {
		close(finish)
		t.Fatal("queued cancellation targeted the active executor", err)
	}
	q := localOriginalQuery(t, f, second, "SELECT 434343")
	if q.SQL.Sets[0].Rows[0][0].Text != "434343" {
		close(finish)
		t.Fatal("independent original session blocked")
	}
	close(finish)
	result := <-done
	recordTransportReceipt(t, f, "queued-original", result.receipt)
	if result.err != nil || !result.receipt.ExecutorAcknowledged || !first.Alive() || !second.Alive() {
		t.Fatal("queued cancellation changed captured executor", result.err)
	}
}
