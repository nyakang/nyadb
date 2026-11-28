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

func replacementNativeRows(t *testing.T, r contract.DataRuntime, table, tags string) []contract.DataEdit {
	t.Helper()
	page, err := r.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var edits []contract.DataEdit
	for _, ref := range page.RowRefs {
		full, err := r.RowReader(context.Background(), 69, "Manticore", table, *ref)
		if err != nil {
			t.Fatal(err)
		}
		full.Values["tags"] = contract.DataValue{Kind: contract.Text, Text: tags}
		edits = append(edits, contract.DataEdit{Kind: "replace", Original: full.Snapshot, Values: full.Values})
	}
	return edits
}

func TestNativePercolateReplaceCompleteOriginalImmutablePlansAndAdmission(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7801)
	table := createNativePercolate(t, q)
	_, r := nativeData(t, s, table)
	ctx := context.Background()
	original := " " + strings.Repeat("猫🙂", 1025) + "\\'original"
	insert := insertPercolateEdit("1", "@title hello", original)
	insert.Values["filters"] = contract.DataValue{Kind: contract.Text, Text: "tag='a'"}
	saveNative(t, r, table, previewNative(t, r, table, insert), 1)
	edits := replacementNativeRows(t, r, table, "replacement "+original)
	base := edits[0]
	if base.Original.Original[2].Text != original {
		t.Fatal("replacement source is a truncated preview")
	}
	for _, mode := range []string{"sparse", "changed-id", "extra", "null", "default", "truncated-original", "foreign", "fingerprint", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			e := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{base}}).Edits[0]
			switch mode {
			case "sparse":
				delete(e.Values, "filters")
			case "changed-id":
				e.Values["id"] = contract.DataValue{Kind: contract.Integer, Text: "2"}
			case "extra":
				e.Values["unknown"] = contract.DataValue{Kind: contract.Text, Text: "x"}
			case "null":
				e.Values["tags"] = contract.DataValue{Kind: contract.Null, Null: true}
			case "default":
				e.Values["tags"] = contract.DataValue{Kind: contract.Text, Default: true}
			case "truncated-original":
				e.Original.Original[2].Truncated = true
			case "foreign":
				e.Original.Ref.Object.ProfileID++
			case "fingerprint":
				*e.Original.Fingerprints[2] = "forged"
			case "oversized":
				e.Values["tags"] = contract.DataValue{Kind: contract.Text, Text: strings.Repeat("x", contract.DataEditMaxBytes+1)}
			}
			p, err := r.PreviewEdits(ctx, 69, "Manticore", table, []contract.DataEdit{e})
			if err == nil {
				p.Close()
				t.Fatal("invalid replacement admitted", mode)
			}
		})
	}
	p := previewNative(t, r, table, base)
	preview := p.Snapshot()
	if preview.Presentation == nil || preview.Presentation.ReplaceLabel == "" || !strings.Contains(preview.Presentation.Hint, "重新创建") || !strings.Contains(preview.NativeJSON, "without atomic compare-and-replace") || !strings.Contains(preview.NativeJSON, "REPLACE INTO") {
		t.Fatal("replacement scope missing", preview)
	}
	base.Values["tags"] = contract.DataValue{Kind: contract.Text, Text: "caller changed"}
	preview.Edits[0].Values["query"] = contract.DataValue{Kind: contract.Text, Text: "forged"}
	preview.Presentation.ReplaceLabel = "forged"
	if p.Snapshot().Presentation.ReplaceLabel == "forged" {
		t.Fatal("aliased confirmation")
	}
	receipt := saveNative(t, r, table, p, 1)
	if receipt.Outcomes[0].Kind != "replace" || receipt.Outcomes[0].GeneratedID != 1 || !receipt.RefreshRequired || receipt.Outcomes[0].Saved != nil {
		t.Fatal(receipt)
	}
	rows := runNative(t, q, "SELECT id,query,tags,filters FROM "+table+" WHERE id=1")
	if len(rows.Rows) != 1 || rows.Rows[0][1] != "@title hello" || rows.Rows[0][2] != "replacement "+original || rows.Rows[0][3] != "tag='a'" {
		t.Fatal("full original fields lost", rows)
	}
	if _, err := r.SubmitEdits(ctx, 69, "Manticore", table, p); err == nil {
		t.Fatal("original plan replayed")
	}
	objectEvidence(t, "percolate-replace-complete-receipt.json", receipt)
	objectEvidence(t, "percolate-replace-complete-readback.json", rows)
	// Replacement cannot turn a bounded preview of an oversized original into
	// a complete record merely by supplying smaller new values.
	runNative(t, q, "INSERT INTO "+table+" (id,query,tags) VALUES (2,'hello','"+strings.Repeat("x", contract.DataEditMaxBytes+1)+"')")
	page, err := r.Loader(ctx, 69, "Manticore", table, 0, 10)
	if err != nil || len(page.Rows) != 2 {
		t.Fatal(err)
	}
	if _, err = r.RowReader(ctx, 69, "Manticore", table, *page.RowRefs[1]); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("oversized original admitted for replacement", err)
	}
	short := contract.DataEdit{Kind: "replace", Original: page.RowSnapshots[1], Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "2"}, "query": {Kind: contract.Text, Text: "hello"}, "tags": {Kind: contract.Text, Text: "short"}, "filters": {Kind: contract.Text}}}
	if _, err = r.PreviewEdits(ctx, 69, "Manticore", table, []contract.DataEdit{short}); err == nil {
		t.Fatal("truncated oversized original admitted")
	}
}

func TestNativePercolateReplaceNativeFailurePrefixAndOriginalRecords(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7802)
	table := createNativePercolate(t, q)
	ctx := context.Background()
	for _, mode := range []string{"query", "filter"} {
		t.Run(mode, func(t *testing.T) {
			runNative(t, q, "REPLACE INTO "+table+" (id,query,tags) VALUES (1,'hello','original'),(2,'hello','original'),(3,'hello','original')")
			_, r := nativeData(t, s, table)
			edits := replacementNativeRows(t, r, table, "replaced")
			if mode == "query" {
				edits[1].Values["query"] = contract.DataValue{Kind: contract.Text, Text: "@missing hello"}
			} else {
				edits[1].Values["filters"] = contract.DataValue{Kind: contract.Text, Text: "missing=1"}
			}
			p := previewNative(t, r, table, edits...)
			receipt, err := r.SubmitEdits(ctx, 69, "Manticore", table, p)
			if err == nil || receipt.Applied != 1 || !receipt.Incomplete || receipt.CommitUncertain || receipt.Sequential.Statements[0].State != contract.DataWriteAcknowledged || receipt.Sequential.Statements[1].State != contract.DataWriteFailed || receipt.Sequential.Statements[2].State != contract.DataWriteNotSent {
				t.Fatal(receipt, err)
			}
			var native nativeWriteReceipt
			if err = json.Unmarshal([]byte(receipt.Sequential.Statements[1].NativeJSON), &native); err != nil || native.ErrorCode != 1064 || native.SQLState != "42000" || !native.Acknowledged {
				t.Fatal(native, err)
			}
			rows := runNative(t, q, "SELECT id,tags FROM "+table+" ORDER BY id ASC")
			if len(rows.Rows) != 3 || rows.Rows[0][1] != "replaced" || rows.Rows[1][1] != "original" || rows.Rows[2][1] != "original" {
				t.Fatal(rows)
			}
			if _, err = r.PreviewEdits(ctx, 69, "Manticore", table, edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
				t.Fatal("incomplete original replayed", err)
			}
			objectEvidence(t, "percolate-replace-"+mode+"-failure.json", receipt)
		})
	}
}

func TestNativePercolateReplaceLostCanceledClosedReceiptAndNoReplay(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7803)
	table := createNativePercolate(t, q)
	for _, mode := range []string{"drop", "cancel", "document-close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			runNative(t, q, "REPLACE INTO "+table+" (id,query,tags) VALUES (1,'hello','original'),(2,'hello','original'),(3,'hello','original')")
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			doc, r := nativeData(t, s, table)
			edits := replacementNativeRows(t, r, table, "replaced")
			p := previewNative(t, r, table, edits...)
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
				t.Fatal("original replacement did not settle")
			}
			v := result.receipt
			if !errors.Is(result.err, contract.ErrDataCommitUncertain) || v.Applied != 1 || !v.Incomplete || v.Sequential.Statements[1].State != contract.DataWriteUnknown || v.Sequential.Statements[2].State != contract.DataWriteNotSent || gate.calls.Load() != 1 {
				t.Fatal(v, result.err)
			}
			original := v.NativeJSON
			disarm(gate)
			rows := runNative(t, q, "SELECT id,tags FROM "+table+" ORDER BY id ASC")
			if len(rows.Rows) != 3 || rows.Rows[0][1] != "replaced" || rows.Rows[1][1] != "replaced" || rows.Rows[2][1] != "original" || v.NativeJSON != original {
				t.Fatal(rows)
			}
			if mode == "drop" || mode == "cancel" {
				if _, err := r.PreviewEdits(context.Background(), 69, "Manticore", table, edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal(err)
				}
			}
			objectEvidence(t, "percolate-replace-"+mode+"-receipt.json", v)
		})
	}
}

func TestNativePercolateReplacePreflightConflictAndConcurrentRecreation(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7804)
	table := createNativePercolate(t, q)
	for _, mode := range []string{"conflict", "retired", "removed", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			runNative(t, q, "REPLACE INTO "+table+" (id,query,tags) VALUES (1,'hello','original')")
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			_, r := nativeData(t, s, table)
			edits := replacementNativeRows(t, r, table, "replacement")
			p := previewNative(t, r, table, edits...)
			ctx := context.Background()
			if mode == "conflict" || mode == "retired" {
				if mode == "conflict" {
					runNative(t, q, "REPLACE INTO "+table+" (id,query,tags) VALUES (1,'world','external')")
				} else if _, err := r.Loader(ctx, 69, "Manticore", table, 0, 10); err != nil {
					t.Fatal(err)
				}
				v, err := r.SubmitEdits(ctx, 69, "Manticore", table, p)
				if !errors.Is(err, contract.ErrDataConflict) || v.Applied != 0 || v.Sequential.Statements[0].State != contract.DataWriteNotSent {
					t.Fatal(v, err)
				}
				objectEvidence(t, "percolate-replace-"+mode+".json", v)
				return
			}
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
				runNative(t, q, "REPLACE INTO "+table+" (id,query,tags) VALUES (1,'world','external')")
			}
			close(gate.resume)
			var result response
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original replacement did not settle")
			}
			v := result.receipt
			if result.err != nil || !contract.DataWriteComplete(v, 1) || v.Sequential.Statements[0].AffectedRows != 1 || v.Outcomes[0].GeneratedID != 1 {
				t.Fatal(v, result.err)
			}
			rows := runNative(t, q, "SELECT id,query,tags FROM "+table+" ORDER BY id ASC")
			if fmt.Sprint(rows.Rows) != "[[1 hello replacement]]" {
				t.Fatal(rows)
			}
			objectEvidence(t, "percolate-replace-after-"+mode+".json", v)
		})
	}
}
