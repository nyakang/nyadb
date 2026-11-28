package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func replacementRTNativeRows(t *testing.T, r contract.DataRuntime, table, tag string) []contract.DataEdit {
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
		full.Values["tag"] = contract.DataValue{Kind: contract.Text, Text: tag}
		edits = append(edits, contract.DataEdit{Kind: "replace", Original: full.Snapshot, Values: full.Values})
	}
	return edits
}

func TestNativeRTReplaceCompleteOriginalAdmission(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7900)
	table := createNativeTable(t, q)
	runNative(t, q, "INSERT INTO "+table+" (id,title,tag) VALUES (1,'hello','original')")
	_, r := nativeData(t, s, table)
	base := replacementRTNativeRows(t, r, table, "replacement")[0]
	for _, mode := range []string{"sparse", "changed-id", "extra", "null-text", "malformed-json", "malformed-null", "default", "truncated-original", "foreign", "fingerprint", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			e := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{base}}).Edits[0]
			switch mode {
			case "sparse":
				delete(e.Values, "metadata")
			case "changed-id":
				e.Values["id"] = contract.DataValue{Kind: contract.Integer, Text: "2"}
			case "extra":
				e.Values["missing"] = contract.DataValue{Kind: contract.Text, Text: "x"}
			case "null-text":
				e.Values["title"] = contract.DataValue{Kind: contract.Null, Null: true}
			case "malformed-json":
				e.Values["metadata"] = contract.DataValue{Kind: contract.JSON, Text: "{bad"}
			case "malformed-null":
				e.Values["metadata"] = contract.DataValue{Kind: contract.Null, Null: true, Text: "{bad"}
			case "default":
				e.Values["metadata"] = contract.DataValue{Kind: contract.Null, Null: true, Default: true}
			case "truncated-original":
				e.Original.Original[1].Truncated = true
			case "foreign":
				e.Original.Ref.Object.ProfileID++
			case "fingerprint":
				*e.Original.Fingerprints[1] = "forged"
			case "oversized":
				e.Values["title"] = contract.DataValue{Kind: contract.Text, Text: strings.Repeat("x", contract.DataEditMaxBytes+1)}
			}
			p, err := r.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{e})
			if err == nil {
				p.Close()
				t.Fatal("invalid complete replacement admitted", mode)
			}
		})
	}
	rows := runNative(t, q, "SELECT id,title,tag FROM "+table)
	if fmt.Sprint(rows.Rows) != "[[1 hello original]]" {
		t.Fatal(rows)
	}
	objectEvidence(t, "rt-replace-rejected-input-readback.json", rows)
}
func TestNativeRTReplaceFullTextTypedAttributesAndNullPreservation(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7901)
	table := createNativeTable(t, q)
	runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN tags multi")
	runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN bigtags multi64")
	runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN flags bool")
	runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN serial bigint")
	_, r := nativeData(t, s, table)
	ctx := context.Background()
	long := " " + strings.Repeat("猫🙂", 1025) + "\\'original"
	first := insertNativeEdit("1", "hello original")
	first.Values["tag"] = contract.DataValue{Kind: contract.Text, Text: long}
	first.Values["price"] = contract.DataValue{Kind: contract.Floating, Text: "1.25"}
	first.Values["qty"] = contract.DataValue{Kind: contract.Integer, Text: "4294967295"}
	first.Values["metadata"] = contract.DataValue{Kind: contract.JSON, Text: `{"x":1}`}
	first.Values["tags"] = contract.DataValue{Kind: contract.Text, Text: "1,2"}
	first.Values["bigtags"] = contract.DataValue{Kind: contract.Text, Text: "-1,9223372036854775807"}
	first.Values["flags"] = contract.DataValue{Kind: contract.Boolean, Text: "true"}
	first.Values["serial"] = contract.DataValue{Kind: contract.Integer, Text: "-9223372036854775808"}
	saveNative(t, r, table, previewNative(t, r, table, first, insertNativeEdit("2", "hello null")), 2)
	page, err := r.Loader(ctx, 69, "Manticore", table, 0, 10)
	if err != nil || !slices.Contains(page.EditKinds, "replace") {
		t.Fatal(err)
	}
	edits := []contract.DataEdit{}
	for i, ref := range page.RowRefs {
		full, e := r.RowReader(ctx, 69, "Manticore", table, *ref)
		if e != nil {
			t.Fatal(e)
		}
		if i == 1 && (full.Values["metadata"].Kind != contract.Null || !full.Values["metadata"].Null) {
			t.Fatal(full.Values)
		}
		full.Values["title"] = contract.DataValue{Kind: contract.Text, Text: "world replaced"}
		edits = append(edits, contract.DataEdit{Kind: "replace", Original: full.Snapshot, Values: full.Values})
	}
	p := previewNative(t, r, table, edits...)
	preview := p.Snapshot()
	if preview.Presentation == nil || !strings.Contains(preview.Presentation.Hint, "重建全文索引") || !strings.Contains(preview.NativeJSON, "quoted JSON null") {
		t.Fatal(preview)
	}
	edits[0].Values["tag"] = contract.DataValue{Kind: contract.Text, Text: "mutated caller"}
	receipt := saveNative(t, r, table, p, 2)
	rows := runNative(t, q, "SELECT id,title,tag,metadata,tags,bigtags,flags,qty,price,serial FROM "+table+" ORDER BY id ASC")
	if len(rows.Rows) != 2 || rows.Rows[0][2] != long || rows.Rows[0][3] != `{"x":1}` || rows.Rows[0][4] != "1,2" || rows.Rows[0][5] != "-1,9223372036854775807" || rows.Rows[0][6] != "1" || rows.Rows[0][7] != "4294967295" || rows.Rows[0][8] != "1.25" || rows.Rows[0][9] != "-9223372036854775808" || rows.Sets[0].Rows[1][3].Kind != contract.Null {
		t.Fatal(rows)
	}
	old := runNative(t, q, "SELECT id FROM "+table+" WHERE MATCH('hello')")
	now := runNative(t, q, "SELECT id FROM "+table+" WHERE MATCH('world')")
	if len(old.Rows) != 0 || len(now.Rows) != 2 {
		t.Fatal(old, now)
	}
	objectEvidence(t, "rt-replace-complete-receipt.json", receipt)
	objectEvidence(t, "rt-replace-complete-readback.json", rows)
}

func TestNativeRTReplaceLostCanceledClosedReceiptAndNoReplay(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7903)
	table := createNativeTable(t, q)
	for _, mode := range []string{"drop", "cancel", "document-close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			runNative(t, q, "REPLACE INTO "+table+" (id,title,tag) VALUES (1,'hello','original'),(2,'hello','original'),(3,'hello','original')")
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			doc, r := nativeData(t, s, table)
			edits := replacementRTNativeRows(t, r, table, "replaced")
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
			rows := runNative(t, q, "SELECT id,tag FROM "+table+" ORDER BY id ASC")
			if len(rows.Rows) != 3 || rows.Rows[0][1] != "replaced" || rows.Rows[1][1] != "replaced" || rows.Rows[2][1] != "original" || v.NativeJSON != original {
				t.Fatal(rows)
			}
			if mode == "drop" || mode == "cancel" {
				if _, err := r.PreviewEdits(context.Background(), 69, "Manticore", table, edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal(err)
				}
			}
			objectEvidence(t, "rt-replace-"+mode+"-receipt.json", v)
		})
	}
}

func TestNativeRTReplaceSchemaConflictAndNativeFailurePrefix(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7906)
	for _, mode := range []string{"before-preflight", "after-preflight"} {
		t.Run(mode, func(t *testing.T) {
			table := createNativeTable(t, q)
			runNative(t, q, "INSERT INTO "+table+" (id,title,tag) VALUES (1,'hello','original'),(2,'hello','original'),(3,'hello','original')")
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			_, r := nativeData(t, s, table)
			edits := replacementRTNativeRows(t, r, table, "replaced")
			for i := range edits {
				edits[i].Values["title"] = contract.DataValue{Kind: contract.Text, Text: "world"}
			}
			p := previewNative(t, r, table, edits...)
			ctx := context.Background()
			if mode == "before-preflight" {
				runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN added integer")
				v, err := r.SubmitEdits(ctx, 69, "Manticore", table, p)
				if !errors.Is(err, contract.ErrDataConflict) || v.Applied != 0 || !v.Incomplete || v.CommitUncertain {
					t.Fatal(v, err)
				}
				for _, step := range v.Sequential.Statements {
					if step.State != contract.DataWriteNotSent {
						t.Fatal(v)
					}
				}
				objectEvidence(t, "rt-replace-schema-conflict.json", v)
				return
			}
			// The second original record has already reached the client proxy.
			// Removing its schema column now causes a real native error packet,
			// after the first independent replacement has been acknowledged.
			source := "SELECT `id`,`title`,`price`,`qty`,`tag`,`metadata` FROM `Manticore`." + quote(table) + " WHERE id=2 LIMIT 2 OPTION max_matches=2"
			observed := gate.arm(source, "hold")
			type response struct {
				receipt *contract.DataEditResult
				err     error
			}
			done := make(chan response, 1)
			go func() { v, e := r.SubmitEdits(ctx, 69, "Manticore", table, p); done <- response{v, e} }()
			observedPacket(t, gate, observed)
			runNative(t, q, "ALTER TABLE "+table+" DROP COLUMN tag")
			close(gate.resume)
			var result response
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("native replacement error did not settle")
			}
			v := result.receipt
			if result.err == nil || v.Applied != 1 || !v.Incomplete || v.CommitUncertain || v.Sequential.Statements[0].State != contract.DataWriteAcknowledged || v.Sequential.Statements[1].State != contract.DataWriteFailed || v.Sequential.Statements[2].State != contract.DataWriteNotSent {
				t.Fatal(v, result.err)
			}
			var original nativeWriteReceipt
			if err := json.Unmarshal([]byte(v.Sequential.Statements[1].NativeJSON), &original); err != nil || original.ErrorCode != 1064 || original.SQLState != "42000" || !original.Acknowledged {
				t.Fatal(original, err)
			}
			rows := runNative(t, q, "SELECT id,title FROM "+table+" ORDER BY id ASC")
			if fmt.Sprint(rows.Rows) != "[[1 world] [2 hello] [3 hello]]" {
				t.Fatal(rows)
			}
			if _, err := r.PreviewEdits(ctx, 69, "Manticore", table, edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
				t.Fatal("incomplete replacement replayed", err)
			}
			objectEvidence(t, "rt-replace-native-error-prefix.json", v)
			objectEvidence(t, "rt-replace-native-error-readback.json", rows)
		})
	}
}

func TestNativeRTReplacePreflightConflictAndConcurrentRecreation(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7904)
	table := createNativeTable(t, q)
	for _, mode := range []string{"conflict", "retired", "removed", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			runNative(t, q, "REPLACE INTO "+table+" (id,title,tag) VALUES (1,'hello','original')")
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			_, r := nativeData(t, s, table)
			edits := replacementRTNativeRows(t, r, table, "replacement")
			p := previewNative(t, r, table, edits...)
			ctx := context.Background()
			if mode == "conflict" || mode == "retired" {
				if mode == "conflict" {
					runNative(t, q, "REPLACE INTO "+table+" (id,title,tag) VALUES (1,'world','external')")
				} else if _, err := r.Loader(ctx, 69, "Manticore", table, 0, 10); err != nil {
					t.Fatal(err)
				}
				v, err := r.SubmitEdits(ctx, 69, "Manticore", table, p)
				if !errors.Is(err, contract.ErrDataConflict) || v.Applied != 0 || v.Sequential.Statements[0].State != contract.DataWriteNotSent {
					t.Fatal(v, err)
				}
				objectEvidence(t, "rt-replace-"+mode+".json", v)
				return
			}
			source := "SELECT `id`,`title`,`price`,`qty`,`tag`,`metadata` FROM `Manticore`." + quote(table) + " WHERE id=1 LIMIT 2 OPTION max_matches=2"
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
				runNative(t, q, "REPLACE INTO "+table+" (id,title,tag) VALUES (1,'world','external')")
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
			rows := runNative(t, q, "SELECT id,title,tag FROM "+table+" ORDER BY id ASC")
			if fmt.Sprint(rows.Rows) != "[[1 hello replacement]]" {
				t.Fatal(rows)
			}
			objectEvidence(t, "rt-replace-after-"+mode+".json", v)
		})
	}
}

func TestNativeRTReplaceHighBitIdentityAndIndexedOnlyAdmission(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7905)
	ctx := context.Background()
	for _, mode := range []string{"rt", "percolate"} {
		t.Run(mode, func(t *testing.T) {
			table := createNativeTable(t, q)
			field, tag := "title", "tag"
			if mode == "percolate" {
				table = createNativePercolate(t, q)
				field, tag = "query", "tags"
			}
			runNative(t, q, "INSERT INTO "+table+" (id,"+field+","+tag+") VALUES (18446744073709551615,'hello','max')")
			_, r := nativeData(t, s, table)
			page, e := r.Loader(ctx, 69, "Manticore", table, 0, 10)
			if e != nil || len(page.Rows) != 1 || page.Rows[0][0].Text != "-1" {
				t.Fatal(page, e)
			}
			full, e := r.RowReader(ctx, 69, "Manticore", table, *page.RowRefs[0])
			if e != nil {
				t.Fatal(e)
			}
			full.Values[field] = contract.DataValue{Kind: contract.Text, Text: "world"}
			edit := contract.DataEdit{Kind: "replace", Original: full.Snapshot, Values: full.Values}
			p := previewNative(t, r, table, edit)
			preview := p.Snapshot()
			if preview.Edits[0].Original.Original[0].Text != "-1" || preview.Edits[0].Values["id"].Text != "-1" || !strings.Contains(preview.NativeJSON, "VALUES (18446744073709551615,") {
				t.Fatal("identity changed or negative sent", preview)
			}
			receipt := saveNative(t, r, table, p, 1)
			// database/sql preserves the original uint64 bit pattern as int64.
			if receipt.Outcomes[0].GeneratedID != -1 {
				t.Fatal(receipt.Outcomes)
			}
			rows := runNative(t, q, "SELECT id,"+field+","+tag+" FROM "+table)
			if len(rows.Rows) != 1 || rows.Rows[0][0] != "-1" || rows.Rows[0][1] != "world" || rows.Rows[0][2] != "max" {
				t.Fatal(rows)
			}
			objectEvidence(t, mode+"-replace-high-id-receipt.json", receipt)
		})
	}
	table := fmt.Sprintf("nyadb_rt79_indexed_%d", time.Now().UnixNano())
	runNative(t, q, "CREATE TABLE "+table+" (title text indexed, tag string)")
	defer runNative(t, q, "DROP TABLE "+table)
	runNative(t, q, "INSERT INTO "+table+" (id,title,tag) VALUES (1,'hello','original')")
	_, r := nativeData(t, s, table)
	m, e := r.TableMetadata(ctx, 69, "Manticore", table)
	if e != nil || slices.Contains(writeKinds(m), "replace") || rtReplacementSupported(m) {
		t.Fatal(m, e)
	}
	_, e = r.PreviewEdits(ctx, 69, "Manticore", table, []contract.DataEdit{{Kind: "replace", Original: &contract.RowSnapshot{}, Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "1"}, "title": {Kind: contract.Text, Text: "world"}, "tag": {Kind: contract.Text, Text: "x"}}}})
	if !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal(e)
	}
	rows := runNative(t, q, "SELECT id FROM "+table+" WHERE MATCH('hello')")
	if len(rows.Rows) != 1 {
		t.Fatal("unreadable source changed")
	}
}
