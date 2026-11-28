package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func insertPercolateEdit(id, query, tags string) contract.DataEdit {
	values := map[string]contract.DataValue{"query": {Kind: contract.Text, Text: query}, "tags": {Kind: contract.Text, Text: tags}}
	if id != "" {
		values["id"] = contract.DataValue{Kind: contract.Integer, Text: id}
	}
	return contract.DataEdit{Kind: "insert", Values: values}
}

func TestNativePercolateWriteOriginalInsertDeletePlansAndPreflight(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7701)
	table := createNativePercolate(t, q)
	_, r := nativeData(t, s, table)
	ctx := context.Background()
	original := " " + strings.Repeat("猫🙂", 1025) + "\\'original"
	first := insertPercolateEdit("1", "@title hello", original)
	first.Values["filters"] = contract.DataValue{Kind: contract.Text, Text: "tag='a'"}
	p := previewNative(t, r, table, first, insertPercolateEdit("", "hello", "generated"))
	preview := p.Snapshot()
	if preview.Presentation == nil || !strings.Contains(preview.Presentation.Hint, "不保证原子比较删除") || !strings.Contains(preview.NativeJSON, "without atomic compare-and-delete") {
		t.Fatal("percolate scope missing", preview)
	}
	preview.Presentation.Hint = "forged"
	preview.Edits[0].Values["tags"] = contract.DataValue{Text: "forged"}
	first.Values["tags"] = contract.DataValue{Text: "caller changed"}
	if p.Snapshot().Presentation.Hint == "forged" {
		t.Fatal("snapshot aliased original confirmation")
	}
	receipt := saveNative(t, r, table, p, 2)
	if receipt.Outcomes[0].GeneratedID != 1 || receipt.Outcomes[1].GeneratedID == 0 {
		t.Fatal(receipt.Outcomes)
	}
	if _, err := r.SubmitEdits(ctx, 69, "Manticore", table, p); err == nil {
		t.Fatal("plan replayed")
	}
	page, err := r.Loader(ctx, 69, "Manticore", table, 0, 10)
	if err != nil || len(page.Rows) != 2 || page.Editable == nil || !*page.Editable {
		t.Fatal(page, err)
	}
	if contract.DataColumnEditAllowed(page, "tags", "update") || !contract.DataColumnEditAllowed(page, "query", "insert") {
		t.Fatal("percolate permissions")
	}
	full, err := r.RowReader(ctx, 69, "Manticore", table, *page.RowRefs[0])
	if err != nil || full.Values["tags"].Text != original || full.Values["filters"].Text != "tag='a'" {
		t.Fatal(full, err)
	}
	if _, err = r.PreviewEdits(ctx, 69, "Manticore", table, []contract.DataEdit{{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"tags": {Text: "wrong"}}}}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("UPDATE became REPLACE", err)
	}
	p = previewNative(t, r, table, contract.DataEdit{Kind: "delete", Original: full.Snapshot})
	if p.(*dataWritePlan).statements[0].SQL != "DELETE FROM `Manticore`."+quote(table)+" WHERE id=1" {
		t.Fatal(p.Snapshot())
	}
	// A changed original query is detected before dispatch. There is no CAS claim
	// covering another change after the final original-record observation.
	runNative(t, q, "REPLACE INTO "+table+" (id,query,tags) VALUES (1,'world','external')")
	conflict, err := r.SubmitEdits(ctx, 69, "Manticore", table, p)
	if !errors.Is(err, contract.ErrDataConflict) || conflict.Applied != 0 || conflict.Sequential.Statements[0].State != contract.DataWriteNotSent {
		t.Fatal(conflict, err)
	}
	if _, err = r.PreviewEdits(ctx, 69, "Manticore", table, []contract.DataEdit{insertPercolateEdit("3", "hello", "blocked")}); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("incomplete plan replayed", err)
	}
	_, fresh := nativeData(t, s, table)
	page, err = fresh.Loader(ctx, 69, "Manticore", table, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	deleted := saveNative(t, fresh, table, previewNative(t, fresh, table, contract.DataEdit{Kind: "delete", Original: page.RowSnapshots[0]}), 1)
	rows := runNative(t, q, "SELECT id FROM "+table+" WHERE id=1")
	if len(rows.Sets[0].Rows) != 0 {
		t.Fatal("original row not deleted")
	}
	omitted := contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "77"}, "tags": {Kind: contract.Text, Text: "omitted query"}}}
	saveNative(t, fresh, table, previewNative(t, fresh, table, omitted), 1)
	rows = runNative(t, q, "SELECT query,tags,filters FROM "+table+" WHERE id=77")
	if len(rows.Rows) != 1 || rows.Rows[0][0] != "" || rows.Rows[0][1] != "omitted query" || rows.Rows[0][2] != "" {
		t.Fatal(rows)
	}
	objectEvidence(t, "percolate-native-insert-receipt.json", receipt)
	objectEvidence(t, "percolate-native-delete-receipt.json", deleted)
	objectEvidence(t, "percolate-native-delete-conflict.json", conflict)
}

func TestNativePercolateWriteNativeValidationAndOriginalFailurePrefix(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7702)
	table := createNativePercolate(t, q)
	ctx := context.Background()
	for _, mode := range []string{"duplicate", "query", "filter"} {
		t.Run(mode, func(t *testing.T) {
			_, r := nativeData(t, s, table)
			id := map[string]int{"duplicate": 100, "query": 200, "filter": 300}[mode]
			middle := insertPercolateEdit(fmt.Sprint(id+1), "hello", "middle")
			if mode == "duplicate" {
				runNative(t, q, fmt.Sprintf("INSERT INTO %s (id,query,tags) VALUES (%d,'hello','exists')", table, id+1))
			}
			if mode == "query" {
				middle.Values["query"] = contract.DataValue{Kind: contract.Text, Text: "@missing hello"}
			}
			if mode == "filter" {
				middle.Values["filters"] = contract.DataValue{Kind: contract.Text, Text: "missing=1"}
			}
			p := previewNative(t, r, table, insertPercolateEdit(fmt.Sprint(id), "hello", "prefix"), middle, insertPercolateEdit(fmt.Sprint(id+2), "hello", "suffix"))
			receipt, err := r.SubmitEdits(ctx, 69, "Manticore", table, p)
			if err == nil || receipt.Applied != 1 || !receipt.Incomplete || receipt.CommitUncertain || receipt.Sequential.Statements[0].State != contract.DataWriteAcknowledged || receipt.Sequential.Statements[1].State != contract.DataWriteFailed || receipt.Sequential.Statements[2].State != contract.DataWriteNotSent {
				t.Fatal(receipt, err)
			}
			var native nativeWriteReceipt
			if err = json.Unmarshal([]byte(receipt.Sequential.Statements[1].NativeJSON), &native); err != nil || !native.Acknowledged || native.ErrorCode != 1064 || native.SQLState != "42000" || native.Error == "" {
				t.Fatal(native, err)
			}
			rows := runNative(t, q, fmt.Sprintf("SELECT id,tags FROM %s WHERE id >= %d AND id <= %d ORDER BY id ASC", table, id, id+2))
			want := 1
			if mode == "duplicate" {
				want = 2
			}
			if len(rows.Rows) != want || rows.Rows[0][1] != "prefix" || want == 2 && rows.Rows[1][1] != "exists" {
				t.Fatal(rows)
			}
			if _, err = r.PreviewEdits(ctx, 69, "Manticore", table, []contract.DataEdit{insertPercolateEdit("999", "hello", "replay")}); !errors.Is(err, contract.ErrDataCommitUncertain) {
				t.Fatal(err)
			}
			objectEvidence(t, "percolate-native-"+mode+"-failure.json", receipt)
		})
	}
}

func TestNativePercolateWriteOriginalLostCanceledClosedReceiptAndNoReplay(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7703)
	table := createNativePercolate(t, q)
	for i, mode := range []string{"drop", "cancel", "document-close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			doc, r := nativeData(t, s, table)
			start := 400 + i*10
			p := previewNative(t, r, table, insertPercolateEdit(fmt.Sprint(start), "hello", "prefix"), insertPercolateEdit(fmt.Sprint(start+1), "hello", "middle"), insertPercolateEdit(fmt.Sprint(start+2), "hello", "suffix"))
			gateMode := "hold"
			if mode == "drop" {
				gateMode = "drop"
			}
			observed := gate.arm(p.(*dataWritePlan).statements[1].SQL, gateMode)
			type response struct {
				receipt *contract.DataEditResult
				err     error
			}
			done := make(chan response, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { v, e := r.SubmitEdits(ctx, 69, "Manticore", table, p); done <- response{v, e} }()
			observedPacket(t, gate, observed)
			switch mode {
			case "cancel":
				cancel()
			case "document-close":
				if err := doc.Close(); err != nil {
					t.Fatal(err)
				}
			case "session-close":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var result response
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original executor did not settle")
			}
			receipt := result.receipt
			if !errors.Is(result.err, contract.ErrDataCommitUncertain) || receipt.Applied != 1 || !receipt.Incomplete || receipt.Sequential.Statements[1].State != contract.DataWriteUnknown || receipt.Sequential.Statements[2].State != contract.DataWriteNotSent || receipt.CommitConfirmed {
				t.Fatal(receipt, result.err)
			}
			if gate.calls.Load() != 1 {
				t.Fatal("original request replayed")
			}
			original := receipt.NativeJSON
			disarm(gate)
			rows := runNative(t, q, fmt.Sprintf("SELECT id,tags FROM %s WHERE id >= %d AND id <= %d ORDER BY id ASC", table, start, start+2))
			if len(rows.Rows) != 2 || rows.Rows[0][1] != "prefix" || rows.Rows[1][1] != "middle" || receipt.NativeJSON != original {
				t.Fatal("readback altered original receipt", rows)
			}
			if mode == "cancel" || mode == "drop" {
				if _, err := r.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{insertPercolateEdit("999", "hello", "replay")}); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal(err)
				}
			}
			objectEvidence(t, "percolate-native-"+mode+"-receipt.json", receipt)
		})
	}
}

func TestNativePercolateDeleteAfterPreflightKeepsOriginalReceiptAndScope(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7704)
	table := createNativePercolate(t, q)
	for _, mode := range []string{"removed", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			runNative(t, q, "REPLACE INTO "+table+" (id,query,tags) VALUES (1,'hello','original')")
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			_, r := nativeData(t, s, table)
			ctx := context.Background()
			page, err := r.Loader(ctx, 69, "Manticore", table, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			p := previewNative(t, r, table, contract.DataEdit{Kind: "delete", Original: page.RowSnapshots[0]})
			source := "SELECT `id`,`query`,`tags`,`filters` FROM `Manticore`." + quote(table) + " WHERE id=1 LIMIT 2 OPTION max_matches=2"
			observed := gate.arm(source, "hold")
			type response struct {
				receipt *contract.DataEditResult
				err     error
			}
			done := make(chan response, 1)
			go func() { v, e := r.SubmitEdits(ctx, 69, "Manticore", table, p); done <- response{v, e} }()
			observedPacket(t, gate, observed)
			if mode == "removed" {
				runNative(t, q, "DELETE FROM "+table+" WHERE id=1")
			} else {
				runNative(t, q, "REPLACE INTO "+table+" (id,query,tags) VALUES (1,'world','changed after preflight')")
			}
			close(gate.resume)
			var result response
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original deletion did not settle")
			}
			v := result.receipt
			if v == nil || v.Applied != 1 || v.CommitUncertain || v.Sequential.Statements[0].State != contract.DataWriteAcknowledged {
				t.Fatal(v, result.err)
			}
			if mode == "removed" {
				if !errors.Is(result.err, contract.ErrDataConflict) || !v.Incomplete || v.Sequential.Statements[0].AffectedRows != 0 {
					t.Fatal(v, result.err)
				}
			} else if result.err != nil || !contract.DataWriteComplete(v, 1) || v.Sequential.Statements[0].AffectedRows != 1 {
				t.Fatal(v, result.err)
			}
			// The second case demonstrates the disclosed native limitation: an id-only
			// DELETE also removes a concurrent replacement after the original preflight.
			rows := runNative(t, q, "SELECT id FROM "+table+" WHERE id=1")
			if len(rows.Rows) != 0 {
				t.Fatal(rows)
			}
			objectEvidence(t, "percolate-native-delete-after-"+mode+".json", v)
		})
	}
}
