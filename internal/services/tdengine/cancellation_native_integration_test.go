package tdengine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNativeTDengineCancelledOriginalWriteReceiptAndExplicitRecovery(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	verifier := nativeLease(t, s, 1)
	db := nativeDatabase(t, verifier, "ns")
	nativeSQL(t, verifier, db, "CREATE TABLE `original_write`(ts TIMESTAMP,v INT)")
	statement := "INSERT INTO " + qualified(db, "original_write") + " VALUES(1704067200123456789,42)"
	held := make(chan json.RawMessage, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var connectionsOpened, writes atomic.Int64
	upgrader := websocket.Upgrader{}
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		downstream, err := upgrader.Upgrade(rw, r, nil)
		if err != nil {
			return
		}
		defer downstream.Close()
		endpoint := "ws://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))) + "/ws"
		upstream, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
		if err != nil {
			return
		}
		defer upstream.Close()
		connectionsOpened.Add(1)
		var request atomic.Uint64
		clientDone := make(chan struct{})
		go func() {
			defer close(clientDone)
			defer upstream.Close()
			defer downstream.Close()
			for {
				kind, raw, e := downstream.ReadMessage()
				if e != nil {
					return
				}
				if kind == websocket.BinaryMessage && len(raw) >= 30 && binary.LittleEndian.Uint64(raw[16:]) == 6 && string(raw[30:]) == statement {
					request.Store(binary.LittleEndian.Uint64(raw))
					writes.Add(1)
				}
				if upstream.WriteMessage(kind, raw) != nil {
					return
				}
			}
		}()
		defer func() { downstream.Close(); upstream.Close(); <-clientDone }()
		for {
			kind, raw, e := upstream.ReadMessage()
			if e != nil {
				return
			}
			var response nativeResponse
			if kind == websocket.TextMessage && json.Unmarshal(raw, &response) == nil && request.Load() != 0 && response.Request == request.Load() && response.Action == "binary_query" {
				held <- append(json.RawMessage(nil), raw...)
				<-release
				return
			}
			if downstream.WriteMessage(kind, raw) != nil {
				return
			}
		}
	}))
	t.Cleanup(proxy.Close)
	u, _ := url.Parse(proxy.URL)
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	throughProxy := p
	throughProxy.Host, throughProxy.Port = u.Hostname(), uint16(port)
	original := nativeSession(t, throughProxy, password)
	executor := nativeLease(t, original, 91)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completion struct {
		result contract.SQLResult
		err    error
	}
	completed := make(chan completion, 1)
	go func() { r, e := executor.Execute(ctx, "", statement); completed <- completion{r, e} }()
	var receipt json.RawMessage
	select {
	case receipt = <-held:
	case <-time.After(15 * time.Second):
		t.Fatal("original native write acknowledgement was not observed")
	}
	var response nativeResponse
	if json.Unmarshal(receipt, &response) != nil || response.Code != 0 || !response.Update || response.Affected != 1 {
		t.Fatal("held frame is not original accepted native write receipt", string(receipt))
	}
	if n, err := s.count(context.Background(), p.ID, db, "original_write"); err != nil || n != 1 {
		t.Fatal("independent original verifier did not observe committed write", n, err)
	}
	cancel()
	var out completion
	select {
	case out = <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("original cancellation did not settle its reader")
	}
	if !errors.Is(out.err, contract.ErrSQLSessionLost) || !errors.Is(out.err, context.Canceled) || len(out.result.Steps) != 1 || out.result.Steps[0].State != "uncertain" || out.result.AffectedRows != 0 || out.result.NativeJSON != "" {
		t.Fatal("lost original acknowledgement was promoted to success or discarded uncertainty", out.err, out.result)
	}
	opened := connectionsOpened.Load()
	if _, err := executor.Execute(context.Background(), "", statement); !errors.Is(err, contract.ErrSQLSessionLost) || connectionsOpened.Load() != opened || writes.Load() != 1 {
		t.Fatal("lost original executor reconnected or replayed", err)
	}
	releaseOnce.Do(func() { close(release) })
	replacement, err := executor.Replace()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replacement.Release)
	if err = replacement.Preconnect(context.Background()); err != nil || writes.Load() != 1 {
		t.Fatal("explicit recovery replayed original write", err)
	}
	if n, err := s.count(context.Background(), p.ID, db, "original_write"); err != nil || n != 1 {
		t.Fatal("recovery changed committed row", n, err)
	}
	nativeEvidence(t, "cancelled-original-write-receipt", struct {
		Receipt json.RawMessage
		Result  contract.SQLResult
		Error   string
		Writes  int64
	}{receipt, out.result, out.err.Error(), writes.Load()})
}

func TestCancelledQueuedLeaseDoesNotAcquireOrDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Session{profile: connections.Profile{ID: 1}, ctx: context.Background(), leases: map[*queryLease]bool{}}
	l, err := s.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	r, err := l.Execute(ctx, "", "INSERT INTO original VALUES(1)")
	if !errors.Is(err, context.Canceled) || r.NativeJSON != "" || len(r.Steps) != 0 || l.(*queryLease).conn != nil || l.(*queryLease).lost {
		t.Fatal("cancelled request acquired/dispatched a physical session", err, r)
	}
	if readonly("SELECT '\\'; DELETE FROM t") {
		t.Fatal("malformed literal admitted by read-only guard")
	}
	if !strings.Contains(safeError(&serverError{1, "secret value"}, "secret").Error(), "[redacted]") {
		t.Fatal("native error leaked original credential")
	}
}
