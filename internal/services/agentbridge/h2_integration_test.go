package agentbridge

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type h2Fixture struct {
	Java         string     `json:"java"`
	Classpath    []string   `json:"classpath"`
	Directory    string     `json:"directory"`
	PasswordFile string     `json:"password_file"`
	Artifacts    []Artifact `json:"artifacts"`
}

func nativeH2Fixture(t *testing.T) (h2Fixture, string) {
	t.Helper()
	if os.Getenv("NYADB_AGENT_H2_IT") != "1" {
		t.Skip("requires the owned real H2 agent fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_AGENT_H2_FIXTURE_FILE"))
	if err != nil {
		t.Fatal("H2 fixture configuration could not be read")
	}
	var f h2Fixture
	if json.Unmarshal(raw, &f) != nil || len(f.Classpath) == 0 || !filepath.IsAbs(f.Directory) {
		t.Fatal("invalid H2 fixture configuration")
	}
	password, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal("H2 fixture credential could not be read")
	}
	return f, strings.TrimSpace(string(password))
}

func nativeH2Client(t *testing.T, f h2Fixture) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := Start(ctx, LaunchSpec{Executable: f.Java, Args: []string{"-Ddbx.agent.jdbc.pool.connectionTimeoutMillis=3000", "-cp", strings.Join(f.Classpath, string(os.PathListSeparator)), "com.dbx.agent.h2.H2Agent"}, Artifacts: f.Artifacts, Started: func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			return ErrLaunch
		}
		fields := strings.Fields(string(stat[end+1:]))
		if len(fields) < 20 {
			return ErrLaunch
		}
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "java": f.Java, "agent": f.Classpath[0]})
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		_, err = journal.Write(append(record, '\n'))
		return err
	}}, Options{CancelWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("original agent close: %v", err)
		}
	})
	if !c.Handshake().Supports("multi_session") || !c.Handshake().Supports("structured_error_v1") {
		t.Fatal("real agent handshake capabilities missing")
	}
	return c
}

func nativeH2Session(t *testing.T, c *Client, password, profile, database, url string) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, r, err := c.OpenSession(ctx, map[string]any{"database": database, "connection_string": url, "username": "sa", "password": password, "driver_profile": profile})
	if err != nil || !r.ExecutorAcknowledged {
		t.Fatalf("real H2 session open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func nativeH2Query(t *testing.T, s *Session, sql string) QuerySnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := s.Call(ctx, "execute_query", map[string]any{"sql": sql, "schema": "PUBLIC", "maxRows": 100, "timeoutSecs": 5})
	if err != nil {
		t.Fatal(err)
	}
	q, err := DecodeQuery(r, nil)
	if err != nil {
		t.Fatalf("native query types require adaptation: %s", r.Result)
	}
	return q
}

func nativeH2Scalar(t *testing.T, s *Session, sql string) string {
	t.Helper()
	q := nativeH2Query(t, s, sql)
	if len(q.SQL.Sets) != 1 || len(q.SQL.Sets[0].Rows) != 1 || len(q.SQL.Sets[0].Rows[0]) != 1 {
		t.Fatal("native scalar shape changed")
	}
	return q.SQL.Sets[0].Rows[0][0].Text
}

func TestH2AgentNativeVersionsValuesMetadataAndIndependentSessions(t *testing.T) {
	f, password := nativeH2Fixture(t)
	for _, version := range []struct{ profile, version string }{{"h2-v1", "1.4.200"}, {"h2-v2", "2.1.214"}, {"h2-v3", "2.4.240"}} {
		t.Run(version.version, func(t *testing.T) {
			c := nativeH2Client(t, f)
			database := "native_" + strings.ReplaceAll(version.version, ".", "_")
			url := "jdbc:h2:mem:" + database + ";DB_CLOSE_DELAY=-1"
			s := nativeH2Session(t, c, password, version.profile, database, url)
			other := nativeH2Session(t, c, password, version.profile, database, url)
			if got := nativeH2Scalar(t, s, "SELECT H2VERSION()"); got != version.version {
				t.Fatalf("expected actual kernel %s; got %s", version.version, got)
			}
			name := "\"quoted\nrecords\""
			nativeH2Query(t, s, "CREATE TABLE "+name+" (\"id\" BIGINT PRIMARY KEY, \"d\" DECIMAL(38,20), \"payload\" VARBINARY, \"note\" VARCHAR, \"empty\" VARBINARY)")
			nativeH2Query(t, s, "INSERT INTO "+name+" VALUES (9007199254740993,123456789012345678.12345678901234567890,X'00FF805C270A',NULL,X'')")
			q := nativeH2Query(t, s, "SELECT * FROM "+name)
			row := q.SQL.Sets[0].Rows[0]
			if row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Text != "123456789012345678.12345678901234567890" || row[2].Display() != "0x00ff805c270a" || row[3].Kind != contract.Null || row[4].Kind != contract.Binary || len(row[4].Bytes) != 0 {
				t.Fatal("real H2 exact values or binary/null identity lost")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			columns, err := other.Call(ctx, "get_columns", map[string]string{"schema": "PUBLIC", "table": "quoted\nrecords"})
			if err != nil {
				t.Fatal(err)
			}
			var nativeColumns []struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(columns.Result, &nativeColumns) != nil || len(nativeColumns) != 5 || nativeColumns[0].Name != "id" {
				t.Fatal("real quoted metadata identity lost")
			}
			ddl, err := other.Call(ctx, "get_table_ddl", map[string]string{"schema": "PUBLIC", "table": "quoted\nrecords"})
			if err != nil {
				t.Fatal(err)
			}
			var definition string
			if json.Unmarshal(ddl.Result, &definition) != nil || !strings.Contains(definition, "PRIMARY KEY") {
				t.Fatalf("native complete table definition shape: %s", ddl.Result)
			}
			// H2 2.x may emit Unicode escaped identifiers. Native replay proves
			// qualified identity instead of inferring it from literal substrings.
			replayDB := database + "_ddl"
			replay := nativeH2Session(t, c, password, version.profile, replayDB, "jdbc:h2:mem:"+replayDB+";DB_CLOSE_DELAY=-1")
			nativeH2Query(t, replay, definition)
			if nativeH2Scalar(t, replay, "SELECT COUNT(*) FROM "+name) != "0" {
				t.Fatal("native definition replay changed identity")
			}
			if _, err := s.Call(ctx, "begin_manual_transaction", map[string]string{"schema": "PUBLIC"}); err != nil {
				t.Fatal(err)
			}
			nativeH2Query(t, s, "INSERT INTO "+name+" (\"id\") VALUES (42)")
			if nativeH2Scalar(t, s, "SELECT COUNT(*) FROM "+name) != "2" || nativeH2Scalar(t, other, "SELECT COUNT(*) FROM "+name) != "1" {
				t.Fatal("transaction escaped its original logical session")
			}
			if _, err := s.Call(ctx, "rollback_manual_transaction", nil); err != nil {
				t.Fatal(err)
			}
			if nativeH2Scalar(t, other, "SELECT COUNT(*) FROM "+name) != "1" {
				t.Fatal("native rollback failed")
			}
			if _, err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if nativeH2Scalar(t, other, "SELECT COUNT(*) FROM "+name) != "1" {
				t.Fatal("close reached another original session")
			}
		})
	}
}

func TestH2AgentNativePinnedStateCursorAndOriginalRuntimeFailure(t *testing.T) {
	f, password := nativeH2Fixture(t)
	c := nativeH2Client(t, f)
	database := "native_state"
	url := "jdbc:h2:mem:" + database + ";DB_CLOSE_DELAY=-1"
	s := nativeH2Session(t, c, password, "h2-v3", database, url)
	other := nativeH2Session(t, c, password, "h2-v3", database, url)
	// A logical ID alone is not a physical lease. This native stateful request
	// exercises the actual agent affinity boundary before checking SESSION_ID.
	nativeH2Query(t, s, "SET SCHEMA PUBLIC")
	id := nativeH2Scalar(t, s, "SELECT SESSION_ID()")
	nativeH2Query(t, s, "CREATE LOCAL TEMPORARY TABLE NYADB_LOCAL (X BIGINT)")
	nativeH2Query(t, s, "INSERT INTO NYADB_LOCAL VALUES (9007199254740993)")
	if nativeH2Scalar(t, s, "SELECT X FROM NYADB_LOCAL") != "9007199254740993" || nativeH2Scalar(t, s, "SELECT SESSION_ID()") != id {
		t.Fatal("pinned physical temporary state changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := other.Call(ctx, "execute_query", map[string]string{"sql": "SELECT * FROM NYADB_LOCAL", "schema": "PUBLIC"}); err == nil {
		t.Fatal("temporary state leaked into another original session")
	}
	page, err := s.Call(ctx, "execute_query_page", map[string]any{"sql": "SELECT X FROM SYSTEM_RANGE(1,4)", "schema": "PUBLIC", "pageSize": 2, "maxRows": 10})
	if err != nil {
		t.Fatal(err)
	}
	var cursor struct {
		ID   string `json:"session_id"`
		More bool   `json:"has_more"`
	}
	if json.Unmarshal(page.Result, &cursor) != nil || cursor.ID == "" || !cursor.More {
		t.Fatal("original native cursor missing")
	}
	if _, err := other.Call(ctx, "fetch_query_page", map[string]any{"sessionId": cursor.ID, "pageSize": 2}); err == nil {
		t.Fatal("native cursor transferred to another logical session")
	}
	next, err := s.Call(ctx, "fetch_query_page", map[string]any{"sessionId": cursor.ID, "pageSize": 2})
	if err != nil {
		t.Fatal(err)
	}
	q, err := DecodeQuery(next, nil)
	if err != nil || len(q.SQL.Sets[0].Rows) != 2 || q.SQL.Sets[0].Rows[0][0].Text != "3" {
		t.Fatal("original cursor continuation lost")
	}
	if nativeH2Scalar(t, s, "SELECT SESSION_ID()") != id {
		t.Fatal("cursor access changed original physical state")
	}
	independent := nativeH2Client(t, f)
	survivor := nativeH2Session(t, independent, password, "h2-v3", "independent_native", "jdbc:h2:mem:independent_native;DB_CLOSE_DELAY=-1")
	_, authReceipt, authErr := c.OpenSession(ctx, map[string]string{"connection_string": url, "database": database, "username": "sa", "password": password + "wrong", "driver_profile": "h2-v3"})
	if authErr == nil {
		t.Fatal("native authentication accepted a wrong password")
	}
	if strings.Contains(authErr.Error(), password) {
		t.Fatal("native authentication exposed a credential")
	}
	// The real v2 H2 pool reports this failed open as physical state unknown
	// and explicitly requires replacement. Honor that receipt for its captured
	// original runtime; neither retry the open nor retire a newer runtime.
	if authReceipt.RemoteError == nil || authReceipt.RemoteError.SessionDisposition != "replace_runtime" || authReceipt.RemoteError.OperationOutcome != "not_started" || !authReceipt.ExecutorAcknowledged || !authReceipt.ProcessExitConfirmed {
		t.Fatal("original native failed-open receipt lost")
	}
	if c.Alive() || s.Alive() || other.Alive() {
		t.Fatal("original native runtime replacement requirement ignored")
	}
	if !survivor.Alive() || nativeH2Scalar(t, survivor, "SELECT 1") != "1" {
		t.Fatal("original native failure reached another captured runtime")
	}
}

func TestH2AgentNativeOwnedFileRestartWithoutReplay(t *testing.T) {
	f, password := nativeH2Fixture(t)
	file := filepath.Join(f.Directory, "owned_native_file_"+rand.Text())
	url := "jdbc:h2:file:" + filepath.ToSlash(file) + ";DB_CLOSE_ON_EXIT=FALSE"
	c := nativeH2Client(t, f)
	s := nativeH2Session(t, c, password, "h2-v3", file, url)
	nativeH2Query(t, s, "CREATE TABLE DURABLE_NATIVE (X BIGINT)")
	nativeH2Query(t, s, "INSERT INTO DURABLE_NATIVE VALUES (1)")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Exited():
	default:
		t.Fatal("original file process exit unconfirmed")
	}
	if r, err := s.Call(context.Background(), "execute_query", map[string]string{"sql": "INSERT INTO DURABLE_NATIVE VALUES (2)"}); !errors.Is(err, ErrClosed) || r.Dispatched {
		t.Fatal("lost original session replayed its write")
	}
	replacement := nativeH2Client(t, f)
	recovered := nativeH2Session(t, replacement, password, "h2-v3", file, url)
	if nativeH2Scalar(t, recovered, "SELECT COUNT(*) FROM DURABLE_NATIVE") != "1" {
		t.Fatal("explicit file recovery lost data or replayed SQL")
	}
}

func TestH2AgentNativeCancelWaitsForExecutorAndPreservesOtherSession(t *testing.T) {
	f, password := nativeH2Fixture(t)
	c := nativeH2Client(t, f)
	database := "native_cancel"
	url := "jdbc:h2:mem:" + database + ";DB_CLOSE_DELAY=-1"
	s := nativeH2Session(t, c, password, "h2-v3", database, url)
	probe := nativeH2Session(t, c, password, "h2-v3", database, url)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		r   Receipt
		err error
	}
	completed := make(chan outcome, 1)
	go func() {
		r, err := s.Call(ctx, "execute_query", map[string]any{"sql": "SELECT /* nyadb_native_cancel */ SUM(SIN(X)) FROM SYSTEM_RANGE(1,1000000000)", "schema": "PUBLIC", "maxRows": 1, "timeoutSecs": 15})
		completed <- outcome{r, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		count := nativeH2Scalar(t, probe, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.SESSIONS WHERE SESSION_ID<>SESSION_ID() AND EXECUTING_STATEMENT LIKE '%nyadb_native_cancel%'")
		n, err := strconv.Atoi(count)
		if err != nil {
			t.Fatal("invalid native executing statement count")
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original native executor was not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	result := <-completed
	if !errors.Is(result.err, context.Canceled) || !result.r.CancellationRequested || !result.r.ExecutorAcknowledged || result.r.ProcessExitConfirmed {
		t.Fatalf("native executor acknowledgment lost: %v", result.err)
	}
	if !probe.Alive() || nativeH2Scalar(t, probe, "SELECT 1") != "1" {
		t.Fatal("cancel reached another original logical session")
	}
}

func nativePoolExecute(t *testing.T, lease contract.QueryLease, source string) contract.SQLResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := lease.Execute(ctx, "", source)
	if err != nil {
		t.Fatalf("original SQL lease execution failed: %v", err)
	}
	if result.NativeJSON == "" {
		t.Fatal("complete native SQL result receipt missing")
	}
	return result
}

func nativePoolScalar(t *testing.T, lease contract.QueryLease, source string) string {
	t.Helper()
	result := nativePoolExecute(t, lease, source)
	if len(result.Sets) != 1 || len(result.Sets[0].Rows) != 1 || len(result.Sets[0].Rows[0]) != 1 {
		t.Fatal("original SQL scalar shape changed")
	}
	return result.Sets[0].Rows[0][0].Display()
}

func TestH2AgentNativeWorkbenchQueryLeasesAndCompleteReceipts(t *testing.T) {
	f, password := nativeH2Fixture(t)
	for _, version := range []struct{ profile, version string }{{"h2-v1", "1.4.200"}, {"h2-v2", "2.1.214"}, {"h2-v3", "2.4.240"}} {
		t.Run(version.version, func(t *testing.T) {
			c := nativeH2Client(t, f)
			database := "workbench_" + strings.ReplaceAll(version.version, ".", "_")
			url := "jdbc:h2:mem:" + database + ";DB_CLOSE_DELAY=-1"
			params := map[string]any{"database": database, "connection_string": url, "username": "sa", "password": password, "driver_profile": version.profile}
			pool, err := NewQueryPool(c, params, QueryConfig{InitializeSQL: `SET SCHEMA "PUBLIC"`, DefaultSchema: "PUBLIC", MaxLeases: 2, MaxRows: 100, TimeoutSeconds: 5, Split: sqltext.SplitPortable})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			params["password"] = password + "changed"
			first, err := pool.ReserveQuery(31)
			if err != nil {
				t.Fatal(err)
			}
			other, err := pool.ReserveQuery(32)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.ReserveQuery(33); !errors.Is(err, contract.ErrSQLSessionLimit) {
				t.Fatal("workbench SQL slot limit ignored")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err = first.Preconnect(ctx); err != nil {
				t.Fatal(err)
			}
			id := nativePoolScalar(t, first, "SELECT SESSION_ID()")
			nativePoolExecute(t, first, "CREATE LOCAL TEMPORARY TABLE NYADB_WORKBENCH_LOCAL (X BIGINT); INSERT INTO NYADB_WORKBENCH_LOCAL VALUES (9007199254740993)")
			if nativePoolScalar(t, first, "SELECT X FROM NYADB_WORKBENCH_LOCAL") != "9007199254740993" || nativePoolScalar(t, first, "SELECT SESSION_ID()") != id {
				t.Fatal("original physical lease lost temporary state")
			}
			if nativePoolScalar(t, other, "SELECT SESSION_ID()") == id {
				t.Fatal("SQL documents share the same physical session")
			}
			if _, err = other.Execute(ctx, "", "SELECT X FROM NYADB_WORKBENCH_LOCAL"); err == nil {
				t.Fatal("native temporary state leaked across document leases")
			}
			if nativePoolScalar(t, other, "SELECT 1") != "1" {
				t.Fatal("a kept SQL error retired an unrelated lease")
			}
			nativePoolExecute(t, first, `CREATE TABLE "shared;records" (X BIGINT PRIMARY KEY, D DECIMAL(38,20), B BLOB, C CLOB)`)
			nativePoolExecute(t, first, "BEGIN")
			result := nativePoolExecute(t, first, `INSERT INTO "shared;records" VALUES (9007199254740993,123456789012345678.12345678901234567890,X'00FF805C270A','中文字段'); SELECT X,D,B,C FROM "shared;records"`)
			if len(result.Steps) != 2 || result.AffectedRows != 1 || len(result.Sets) != 1 {
				t.Fatal("complete native statement outcomes or affected rows lost")
			}
			row := result.Sets[0].Rows[0]
			if row[0].Text != "9007199254740993" || row[1].Text != "123456789012345678.12345678901234567890" || row[2].Display() != "0x00ff805c270a" || row[3].Text != "中文字段" {
				t.Fatal("native workbench typed numeric/BLOB/CLOB values lost")
			}
			if nativePoolScalar(t, other, `SELECT COUNT(*) FROM "shared;records"`) != "0" {
				t.Fatal("uncommitted workbench write escaped its original physical lease")
			}
			nativePoolExecute(t, first, "COMMIT")
			if nativePoolScalar(t, other, `SELECT COUNT(*) FROM "shared;records"`) != "1" {
				t.Fatal("original workbench commit failed")
			}
			nativePoolExecute(t, first, "BEGIN")
			nativePoolExecute(t, first, `INSERT INTO "shared;records" (X) VALUES (42)`)
			nativePoolExecute(t, first, "ROLLBACK")
			if nativePoolScalar(t, other, `SELECT COUNT(*) FROM "shared;records"`) != "1" {
				t.Fatal("workbench rollback failed")
			}
			replacement, err := first.Replace()
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(first.(contract.QueryLeaseStatus).CheckQueryLease(), contract.ErrRevoked) {
				t.Fatal("released original lease still admits work")
			}
			if _, err = replacement.Execute(ctx, "", "SELECT X FROM NYADB_WORKBENCH_LOCAL"); err == nil {
				t.Fatal("replacement replayed original temporary state")
			}
			if nativePoolScalar(t, replacement, `SELECT COUNT(*) FROM "shared;records"`) != "1" {
				t.Fatal("replacement lost the original database identity")
			}
			metadata := nativeH2Session(t, c, password, version.profile, database, url)
			pool.Close()
			if _, err = pool.ReserveQuery(34); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("closed query pool admitted a SQL document")
			}
			if nativeH2Scalar(t, metadata, "SELECT 1") != "1" {
				t.Fatal("query pool close reached the borrowed metadata runtime")
			}
		})
	}
}

func TestH2AgentNativeWorkbenchQueuedCancellationAndOriginalReceipt(t *testing.T) {
	f, password := nativeH2Fixture(t)
	c := nativeH2Client(t, f)
	database := "workbench_cancel"
	url := "jdbc:h2:mem:" + database + ";DB_CLOSE_DELAY=-1"
	pool, err := NewQueryPool(c, map[string]any{"database": database, "connection_string": url, "username": "sa", "password": password, "driver_profile": "h2-v3"}, QueryConfig{InitializeSQL: `SET SCHEMA "PUBLIC"`, DefaultSchema: "PUBLIC", MaxLeases: 2, MaxRows: 10, TimeoutSeconds: 15, Split: sqltext.SplitPortable})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	lease, err := pool.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	probe := nativeH2Session(t, c, password, "h2-v3", database, url)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		_, err := lease.Execute(ctx, "", "SELECT /* nyadb_workbench_cancel */ SUM(SIN(X)) FROM SYSTEM_RANGE(1,1000000000)")
		completed <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		count := nativeH2Scalar(t, probe, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.SESSIONS WHERE SESSION_ID<>SESSION_ID() AND EXECUTING_STATEMENT LIKE '%nyadb_workbench_cancel%'")
		if count == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original workbench executor did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	queuedCtx, queuedCancel := context.WithCancel(context.Background())
	queuedCancel()
	queued := make(chan error, 1)
	go func() { _, err := lease.Execute(queuedCtx, "", "CREATE TABLE MUST_NOT_DISPATCH(X INT)"); queued <- err }()
	if count := nativeH2Scalar(t, probe, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.SESSIONS WHERE SESSION_ID<>SESSION_ID() AND EXECUTING_STATEMENT LIKE '%nyadb_workbench_cancel%'"); count != "1" {
		t.Fatal("queued cancellation reached the original running executor")
	}
	if err = lease.(contract.QueryLeaseStatus).CheckQueryLease(); err != nil {
		t.Fatal("lease status blocked or revoked the running executor", err)
	}
	cancel()
	if err = <-completed; !errors.Is(err, context.Canceled) {
		t.Fatal("original workbench cancellation was not preserved", err)
	}
	if err = <-queued; !errors.Is(err, context.Canceled) {
		t.Fatal("queued canceled statement was dispatched", err)
	}
	receipts := lease.(interface{ OriginalQueryReceipts() []Receipt }).OriginalQueryReceipts()
	if len(receipts) != 1 || !receipts[0].CancellationRequested || !receipts[0].ExecutorAcknowledged || receipts[0].ProcessExitConfirmed {
		t.Fatal("cancel acknowledgement replaced the original workbench executor receipt")
	}
	if nativeH2Scalar(t, probe, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_NAME='MUST_NOT_DISPATCH'") != "0" {
		t.Fatal("queued canceled DDL ran")
	}
	if nativeH2Scalar(t, probe, "SELECT 1") != "1" {
		t.Fatal("workbench cancel retired the original metadata session")
	}
}

func TestH2AgentNativeTypedCursorsKeepOriginalIdentityAndCloseScopes(t *testing.T) {
	f, password := nativeH2Fixture(t)
	for _, version := range []struct{ profile, version string }{{"h2-v1", "1.4.200"}, {"h2-v2", "2.1.214"}, {"h2-v3", "2.4.240"}} {
		t.Run(version.version, func(t *testing.T) {
			c := nativeH2Client(t, f)
			database := "typed_cursor_" + strings.ReplaceAll(version.version, ".", "_")
			url := "jdbc:h2:mem:" + database + ";DB_CLOSE_DELAY=-1"
			s := nativeH2Session(t, c, password, version.profile, database, url)
			other := nativeH2Session(t, c, password, version.profile, database, url)
			nativeH2Query(t, s, "SET SCHEMA PUBLIC")
			id := nativeH2Scalar(t, s, "SELECT SESSION_ID()")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cursor, page, err := s.OpenQueryCursor(ctx, QueryCursorOptions{SQL: "SELECT X,CAST(X'00FF' AS BLOB) AS B,CAST('中文' AS CLOB) AS C FROM SYSTEM_RANGE(1,5)", Schema: "PUBLIC", PageSize: 2, MaxRows: 20, TimeoutSeconds: 5})
			if err != nil || !page.HasMore || !cursor.HasMore() || page.SQL.Rows[0][0] != "1" || page.SQL.Sets[0].Rows[0][1].Display() != "0x00ff" || page.SQL.Sets[0].Rows[0][2].Text != "中文" {
				t.Fatal("native typed cursor first page lost", err)
			}
			t.Cleanup(func() { cursor.Close() })
			original := cursor.id
			if _, err = other.Call(ctx, "fetch_query_page", map[string]any{"sessionId": original, "pageSize": 2}); err == nil {
				t.Fatal("native cursor transferred to another original logical session")
			}
			next, err := cursor.Fetch(ctx, 2)
			if err != nil || next.SQL.Rows[0][0] != "3" || !next.HasMore || !next.Receipt.ExecutorAcknowledged {
				t.Fatal("typed cursor continuation lost", err)
			}
			terminal, err := cursor.Fetch(ctx, 2)
			if err != nil || terminal.SQL.Rows[0][0] != "5" || terminal.HasMore || cursor.HasMore() {
				t.Fatal("native cursor terminal receipt lost", err)
			}
			if err = cursor.Close(); err != nil {
				t.Fatal("terminal cursor close failed", err)
			}
			if _, err = cursor.Fetch(ctx, 2); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("terminal closed cursor accepted work")
			}
			if nativeH2Scalar(t, s, "SELECT SESSION_ID()") != id {
				t.Fatal("typed cursor changed the original physical lease")
			}
			active, _, err := s.OpenQueryCursor(ctx, QueryCursorOptions{SQL: "SELECT X FROM SYSTEM_RANGE(1,100)", Schema: "PUBLIC", PageSize: 1, MaxRows: 200})
			if err != nil {
				t.Fatal(err)
			}
			if err = active.Close(); err != nil {
				t.Fatal(err)
			}
			if nativeH2Scalar(t, other, "SELECT 1") != "1" {
				t.Fatal("cursor disposal closed an unrelated original session")
			}
			if nativeH2Scalar(t, s, "SELECT SESSION_ID()") != id {
				t.Fatal("closing an active cursor disposed its original SQL physical lease")
			}
		})
	}
}
