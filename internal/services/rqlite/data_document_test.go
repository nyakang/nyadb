package rqlite

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNativeTextParameterPreservesAllOriginalUTF8Bytes(t *testing.T) {
	for _, value := range []string{"", "plain", "数据库🐱", "a\x00b", "X'61'", "带引号\"的表"} {
		parameter := textParameter(value)
		if len(parameter) != len(value) {
			t.Fatal("native text binding changed its original byte length")
		}
		for i := range parameter {
			if parameter[i] != int(value[i]) {
				t.Fatal("native text binding skipped part of an original UTF-8 code point")
			}
		}
	}
}

func TestNamespaceResolvesOnlyOriginalMainCatalog(t *testing.T) {
	s := mockSession(t, func(http.ResponseWriter, *http.Request) { t.Error("namespace resolution performed I/O") }, nil)
	for _, name := range []string{"", "main"} {
		if ref := s.Namespace(name); ref != (contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: "main"}) {
			t.Fatal("original fixed catalog lost", ref)
		}
	}
	if ref := s.Namespace("foreign"); ref.Catalog != "" {
		t.Fatal("foreign namespace fabricated", ref)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if ref := s.Namespace("main"); ref.Catalog != "" {
		t.Fatal("closed original namespace remained available", ref)
	}
}

func TestDataDocumentIndependentReadsAndCloseWaitOriginalAcknowledgement(t *testing.T) {
	started, ack := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ack) }) }
	defer release()
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Original-Page") != "" {
			t.Error("unexpected synthetic request identity")
		}
		// Only the page request is held. The original count can finish while
		// it is parked; neither scope can cancel the other's executor.
		if r.URL.Query().Has("transaction") {
			close(started)
			<-ack
		}
		fmt.Fprint(w, `{"results":[{"columns":["observed"],"types":["integer"],"values":[[1]]}]}`)
	}, nil)
	o := contract.ObjectRef{ProfileID: s.ProfileID(), Catalog: "main", Kind: "table", Name: "original"}
	reserved, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	d := reserved.(*dataDocument)
	runtime := d.Runtime()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := dataCall(d, ctx, s.ProfileID(), "main", o.Name, func(ctx context.Context) (receipt, error) {
			return s.request(ctx, "/db/query", []any{"SELECT 1"}, true, false)
		})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("original page did not dispatch")
	}
	cancel()
	_, err = dataCall(d, context.Background(), s.ProfileID(), "main", o.Name, func(ctx context.Context) (receipt, error) {
		return s.request(ctx, "/db/query", []any{"SELECT 1"}, false, false)
	})
	if err != nil {
		t.Fatal("canceling the original page revoked an independent count")
	}
	closed, secondClose, sessionClosed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { _ = d.Close(); close(closed) }()
	go func() { _ = d.Close(); close(secondClose) }()
	go func() { _ = s.Close(); close(sessionClosed) }()
	select {
	case <-closed:
		t.Fatal("document close returned before the original executor acknowledgement")
	case <-secondClose:
		t.Fatal("concurrent document close returned before the original acknowledgement")
	case <-sessionClosed:
		t.Fatal("session close discarded the original document executor")
	case <-result:
		t.Fatal("caller cancellation consumed an unacknowledged page")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	for _, done := range []<-chan struct{}{closed, secondClose, sessionClosed} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("close did not settle after the original acknowledgement")
		}
	}
	if err := <-result; !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed document published its revoked result")
	}
	if _, err := runtime.Count(context.Background(), s.ProfileID(), "main", o.Name); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("cached runtime admitted a request after close")
	}
	s.mu.Lock()
	remaining := len(s.documents)
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("original document registration leaked")
	}
}

func TestDataDocumentReservationIdentityAndCopiedReadReference(t *testing.T) {
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("pure reservation or invalid identity performed I/O")
	}, nil)
	o := contract.ObjectRef{ProfileID: s.ProfileID(), Catalog: "main", Kind: "table", Name: "original"}
	d, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := d.Runtime()
	if _, err := r.Count(context.Background(), s.ProfileID()+1, "main", o.Name); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("original profile identity was not checked before I/O")
	}
	if _, err := r.TableMetadata(context.Background(), s.ProfileID(), "main", "replacement"); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("original relation identity was not checked before I/O")
	}
	ref := contract.RowRef{Object: o, Columns: []string{"key"}, Values: [][]byte{{0, 255}}, TypedValues: []contract.Cell{{Kind: contract.Binary, Bytes: []byte{0, 255}}}}
	copy := copyDataRef(ref)
	ref.Columns[0], ref.Values[0][0], ref.TypedValues[0].Bytes[0] = "changed", 1, 1
	if copy.Columns[0] != "key" || copy.Values[0][0] != 0 || copy.TypedValues[0].Bytes[0] != 0 {
		t.Fatal("captured original reference aliases mutable request bytes")
	}
}
