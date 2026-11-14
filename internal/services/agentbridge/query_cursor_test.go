package agentbridge

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestQueryCursorRetainsOriginalIDAndRejectsFetchAfterExhaustion(t *testing.T) {
	c := helperClient(t, "query_cursor", Options{})
	s := helperSession(t, c)
	cursor, page, err := s.OpenQueryCursor(context.Background(), QueryCursorOptions{SQL: "SELECT exact", PageSize: 2, MaxRows: 10})
	if err != nil || !cursor.HasMore() || !page.HasMore || !page.Receipt.ExecutorAcknowledged {
		t.Fatal("original query cursor was not admitted", err)
	}
	defer cursor.Close()
	page.SQL.Sets[0].Columns[0].Name = "mutated"
	page.Native[0] = ' '
	next, err := cursor.Fetch(context.Background(), 2)
	if err != nil || next.HasMore || cursor.HasMore() || next.SQL.Sets[0].Columns[0].Name != "exact" || next.SQL.Rows[0][0] != "3" {
		t.Fatal("original cursor continuation or independent page lost", err)
	}
	if _, err = cursor.Fetch(context.Background(), 2); !errors.Is(err, io.EOF) {
		t.Fatal("exhausted cursor attempted reexecution", err)
	}
	if err = cursor.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = cursor.Fetch(context.Background(), 2); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original cursor accepted a read")
	}
}

func TestQueryCursorContradictoryNativeIdentityRetiresOnlyCapturedRuntime(t *testing.T) {
	c := helperClient(t, "query_cursor_wrong_id", Options{})
	s := helperSession(t, c)
	other := helperClient(t, "query_cursor", Options{})
	cursor, _, err := s.OpenQueryCursor(context.Background(), QueryCursorOptions{SQL: "SELECT exact", PageSize: 2, MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.Close()
	page, err := cursor.Fetch(context.Background(), 2)
	if !errors.Is(err, ErrProtocol) || page.Receipt.Result == nil || !page.Receipt.ExecutorAcknowledged || c.Alive() || !other.Alive() {
		t.Fatal("contradictory ID reached a replacement runtime or discarded original receipt", err)
	}
	select {
	case <-c.Exited():
	default:
		t.Fatal("cursor protocol fault returned before original process disposal")
	}
}

func TestQueryPageIdentityRejectsMissingOrAmbiguousContinuation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"has_more":null}`, `{"has_more":true}`, `{"has_more":true,"session_id":""}`, `{"has_more":true,"has_more":false,"session_id":"original"}`} {
		if _, _, err := pageIdentity([]byte(raw)); !errors.Is(err, ErrProtocol) {
			t.Fatal("ambiguous page identity accepted")
		}
	}
}
