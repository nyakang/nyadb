package dpiworker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func TestWorkerOutputsOwnDestinationsAndPublishCompleteAcknowledgements(t *testing.T) {
	c := testConn(t)
	key, text := int64(42), "original"
	nullKey := sql.NullInt64{Int64: 43, Valid: true}
	nullText := sql.NullString{String: "stale", Valid: true}
	args := []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &key}}, {Ordinal: 2, Value: sql.Out{Dest: &text}}, {Ordinal: 3, Value: sql.Out{Dest: &nullKey}}, {Ordinal: 4, Value: sql.Out{Dest: &nullText}}}
	wire, err := encodeValues(args)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := decodeValues(wire)
	if err != nil {
		t.Fatal(err)
	}
	if owned[0].Value.(sql.Out).Dest == &key || owned[1].Value.(sql.Out).Dest == &text {
		t.Fatal("worker shares caller destinations")
	}
	if _, err = c.ExecContext(context.Background(), "out-success", args); err != nil || key != 9007199254740993 || text != "数据库 🌸" || nullKey.Valid || nullText.Valid {
		t.Fatal("incomplete IPC outputs", err, key, text, nullKey, nullText)
	}
	stmt, err := c.PrepareContext(context.Background(), "out-success")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for range 2 {
		key, text = 42, "original"
		if _, err = stmt.(driver.StmtExecContext).ExecContext(context.Background(), args); err != nil || key != 9007199254740993 || text != "数据库 🌸" {
			t.Fatal("prepared output reuse failed", err)
		}
	}
}

func TestWorkerMalformedOutputAcknowledgementRetiresOnlyOriginalSession(t *testing.T) {
	for _, outputs := range [][]value{
		nil,
		{{Kind: "int64", Text: "9007199254740993"}},
		{{Kind: "int64", Text: "9007199254740993"}, {Kind: "bytes", Bytes: []byte{1}}},
		{{Kind: "int64", Text: "9007199254740993"}, {Kind: "text", Text: strings.Repeat("x", dpi.MaxOutputTextBytes+1)}},
		{{Kind: "int64", Text: "9007199254740993"}, {Kind: "text", Text: "changed"}, {Kind: "null"}},
	} {
		c, peer := testConn(t), testConn(t)
		key, text := int64(42), "original"
		// Replace this captured call's ACK while its real child is parked.
		c.replies <- reply{response: response{ID: c.next + 1, Valid: true, Outputs: outputs}}
		_, err := c.ExecContext(context.Background(), "hang", []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &key}}, {Ordinal: 2, Value: sql.Out{Dest: &text}}})
		if !errors.Is(err, dpi.ErrOutcomeUnknown) || !errors.Is(err, dpi.ErrSessionClosed) || errors.Is(err, driver.ErrBadConn) || key != 42 || text != "original" || c.IsValid() {
			t.Fatal("malformed ACK partially published outputs", err, key, text)
		}
		if err = peer.Ping(context.Background()); err != nil {
			t.Fatal("malformed ACK retired another worker", err)
		}
	}
}

func TestWorkerOutputsRemainUnchangedOnCancellationLossAndInvalidRequests(t *testing.T) {
	for _, query := range []string{"lose", "wait", "native-error"} {
		c := testConn(t)
		key := int64(42)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_, err := c.ExecContext(ctx, query, []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &key}}})
		if err == nil || errors.Is(err, driver.ErrBadConn) || key != 42 {
			t.Fatal("failed execution published outputs", query, err, key)
		}
		if query != "native-error" && !errors.Is(err, dpi.ErrOutcomeUnknown) {
			t.Fatal("submitted failure lost uncertainty", err)
		}
	}
	c := testConn(t)
	key := int64(42)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ExecContext(ctx, "out-success", []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &key}}})
	if !errors.Is(err, context.Canceled) || errors.Is(err, dpi.ErrOutcomeUnknown) || key != 42 {
		t.Fatal("pre-canceled request published output", err)
	}
	for _, out := range []sql.Out{{}, {Dest: (*int64)(nil)}, {Dest: new(int)}, {Dest: &key, In: true}} {
		if _, err = c.ExecContext(context.Background(), "lose", []driver.NamedValue{{Ordinal: 1, Value: out}}); err == nil {
			t.Fatal("invalid output request submitted")
		}
	}
	if _, err = c.QueryContext(context.Background(), "lose", []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &key}}}); err == nil {
		t.Fatal("query output was submitted")
	}
	if err = c.Ping(context.Background()); err != nil {
		t.Fatal("invalid requests retired original worker", err)
	}
	for _, v := range []value{{Kind: "out", Text: "text", Bytes: []byte{1}}, {Kind: "out", Text: "pointer-address"}} {
		if _, err := decodeValues([]value{v}); err == nil {
			t.Fatal("malformed output descriptor accepted")
		}
	}
	tooMany := make([]driver.NamedValue, dpi.MaxOutputParameters+1)
	for i := range tooMany {
		tooMany[i] = driver.NamedValue{Ordinal: i + 1, Value: sql.Out{Dest: &key}}
	}
	if _, err := encodeValues(tooMany); err == nil {
		t.Fatal("unbounded output acknowledgement accepted")
	}
	wire := make([]value, len(tooMany))
	for i := range wire {
		wire[i] = value{Kind: "out", Text: "text"}
	}
	if _, err := decodeValues(wire); err == nil {
		t.Fatal("unbounded worker output allocation accepted")
	}
}
