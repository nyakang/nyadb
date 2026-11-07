package trino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const testID = "20261108_010203_00001_abcde"
const testColumns = `[{"name":"n","type":"bigint","typeSignature":{"rawType":"bigint","arguments":[]}}]`

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(handler)
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{config: connections.TrinoConfig{Endpoint: s.URL, Username: "original"}, http: s.Client(), ctx: ctx, cancel: cancel, headers: http.Header{}}
	c.headers.Set("X-Trino-User", "original")
	c.headers.Set("X-Trino-Transaction-Id", "NONE")
	t.Cleanup(func() { c.Close(); s.Close() })
	return c, s
}

func TestNativePaginationAndSessionHeadersPreserveRawValues(t *testing.T) {
	var origin string
	var posts atomic.Int64
	c, s := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			n := posts.Add(1)
			if n == 1 {
				w.Header().Set("X-Trino-Set-Catalog", "memory")
				w.Header().Set("X-Trino-Set-Schema", "default")
				w.Header().Set("X-Trino-Set-Session", "query_max_run_time=2h")
				w.Header().Set("X-Trino-Added-Prepare", url.QueryEscape("stmt space")+"="+url.QueryEscape("SELECT 'a,b'\n"))
				w.Header().Set("X-Trino-Started-Transaction-Id", "transaction-token")
				fmt.Fprintf(w, `{"id":%q,"nextUri":%q}`, testID, origin+"/v1/statement/queued/"+testID+"/token/0")
				return
			}
			if r.Header.Get("X-Trino-Catalog") != "memory" || r.Header.Get("X-Trino-Schema") != "default" || r.Header.Get("X-Trino-Session") != "query_max_run_time=2h" || r.Header.Get("X-Trino-Transaction-Id") != "transaction-token" || r.Header.Get("X-Trino-Prepared-Statement") != "stmt+space=SELECT+%27a%2Cb%27%0A" {
				t.Error("native response state was not applied to next statement")
			}
			w.Header().Set("X-Trino-Clear-Session", "query_max_run_time")
			w.Header().Set("X-Trino-Deallocated-Prepare", "stmt+space")
			w.Header().Set("X-Trino-Clear-Transaction-Id", "true")
			fmt.Fprintf(w, `{"id":%q,"updateType":"ROLLBACK"}`, testID)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"columns":%s,"data":[[9007199254740993],[9223372036854775807]]}`, testID, testColumns)
	})
	origin = s.URL
	var values []string
	_, err := c.Run(context.Background(), "SELECT n", func(cols []Column, row []json.RawMessage) error {
		values = append(values, string(row[0]))
		cols[0].Signature[0] = 'x'
		return nil
	})
	if err != nil || strings.Join(values, ",") != "9007199254740993,9223372036854775807" {
		t.Fatal("native raw integer or column isolation lost", err, values)
	}
	_, err = c.Run(context.Background(), "ROLLBACK", nil)
	if err != nil || c.headers.Get("X-Trino-Session") != "" || c.headers.Get("X-Trino-Prepared-Statement") != "" || c.headers.Get("X-Trino-Transaction-Id") != "NONE" {
		t.Fatal("native reset state was retained", err)
	}
}

func TestUncertainInitialPostNeverReplays(t *testing.T) {
	var committed atomic.Int64
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { committed.Add(1); w.WriteHeader(502) })
	_, err := c.Run(context.Background(), "INSERT INTO t VALUES(1)", nil)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || interrupted.CancelAcknowledged || interrupted.QueryID != "" || !errors.Is(err, ErrLost) {
		t.Fatal("unknown initial response looked acknowledged", err)
	}
	_, err = c.Run(context.Background(), "INSERT INTO t VALUES(1)", nil)
	if !errors.Is(err, ErrLost) || committed.Load() != 1 {
		t.Fatal("uncertain command was replayed", err, committed.Load())
	}
}

func TestCloseRollsBackOnlyCapturedOriginalTransaction(t *testing.T) {
	var rolledBack atomic.Int64
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Trino-Transaction-Id") != "original-token" {
			t.Error("cleanup lost original transaction")
		}
		sql, _ := io.ReadAll(r.Body)
		if string(sql) != "ROLLBACK" {
			t.Error("cleanup changed user SQL")
		}
		rolledBack.Add(1)
		w.Header().Set("X-Trino-Clear-Transaction-Id", "true")
		fmt.Fprintf(w, `{"id":%q,"updateType":"ROLLBACK"}`, testID)
	})
	c.headers.Set("X-Trino-Transaction-Id", "original-token")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil || rolledBack.Load() != 1 {
		t.Fatal("close cleanup replayed", err)
	}
}

func TestCancellationAndCloseWaitForCoordinatorAcknowledgement(t *testing.T) {
	getStarted, deleteStarted, releaseDelete := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var origin string
	c, s := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			fmt.Fprintf(w, `{"id":%q,"nextUri":%q}`, testID, origin+"/v1/statement/executing/"+testID+"/token/0")
		case http.MethodGet:
			close(getStarted)
			<-r.Context().Done()
		case http.MethodDelete:
			close(deleteStarted)
			<-releaseDelete
			w.WriteHeader(204)
		}
	})
	origin = s.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Run(ctx, "SELECT long_query", nil); done <- err }()
	<-getStarted
	cancel()
	<-deleteStarted
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-done:
		t.Fatal("cancel returned before DELETE acknowledgement")
	case <-closed:
		t.Fatal("close released an active worker")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseDelete)
	var interrupted *Interrupted
	if err := <-done; !errors.As(err, &interrupted) || !interrupted.CancelAcknowledged || !errors.Is(err, context.Canceled) {
		t.Fatal("confirmed cancellation lost", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not finish after acknowledgement")
	}
}

func TestUntrustedContinuationCannotReceiveCredentials(t *testing.T) {
	var requests atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer foreign.Close()
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":%q,"nextUri":%q}`, testID, foreign.URL+"/v1/statement/executing/"+testID+"/token/0")
	})
	c.password = "private secret"
	_, err := c.Run(context.Background(), "SELECT 1", nil)
	if !errors.Is(err, ErrProtocol) || !errors.Is(err, ErrLost) || requests.Load() != 0 {
		t.Fatal("foreign nextUri followed", err, requests.Load())
	}
}

func TestTerminalNativeErrorPreservesSessionAndHidesLiterals(t *testing.T) {
	var calls atomic.Int64
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprintf(w, `{"id":%q,"error":{"errorCode":1,"errorName":"SYNTAX_ERROR","errorType":"USER_ERROR","message":"private literal"}}`, testID)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"columns":%s,"data":[[1]]}`, testID, testColumns)
	})
	_, err := c.Run(context.Background(), "bad SQL", nil)
	var queryErr *QueryError
	if !errors.As(err, &queryErr) || strings.Contains(err.Error(), "private") {
		t.Fatal("native diagnostics leaked or lost", err)
	}
	if _, err = c.Run(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatal("acknowledged query error retired session", err)
	}
}

func TestNativeMalformedPageAndBoundsRevokeSession(t *testing.T) {
	for _, body := range []string{
		`{"id":"foreign"}`,
		fmt.Sprintf(`{"id":%q,"columns":%s,"data":[[1,2]]}`, testID, testColumns),
		fmt.Sprintf(`{"id":%q,"columns":%s,"data":{"encoding":"json"}}`, testID, testColumns),
		fmt.Sprintf(`{"id":%q,"columns":%s,"data":[[%q]]}`, testID, testColumns, strings.Repeat("x", (1<<20)+1)),
	} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, err := c.Run(context.Background(), "SELECT 1", nil); !errors.Is(err, ErrLost) {
				t.Fatal("malformed or oversized result retained session", err)
			}
		})
	}
}
