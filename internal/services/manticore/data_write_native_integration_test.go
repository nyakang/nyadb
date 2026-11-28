package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func insertNativeEdit(id, title string) contract.DataEdit {
	v := map[string]contract.DataValue{"title": {Kind: contract.Text, Text: title}}
	if id != "" {
		v["id"] = contract.DataValue{Kind: contract.Integer, Text: id}
	}
	return contract.DataEdit{Kind: "insert", Values: v}
}
func previewNative(t *testing.T, r contract.DataRuntime, table string, edits ...contract.DataEdit) contract.DataMutationPlan {
	t.Helper()
	p, e := r.PreviewEdits(context.Background(), 69, "Manticore", table, edits)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func saveNative(t *testing.T, r contract.DataRuntime, table string, p contract.DataMutationPlan, n int) *contract.DataEditResult {
	t.Helper()
	out, e := r.SubmitEdits(context.Background(), 69, "Manticore", table, p)
	if e != nil || !contract.DataWriteComplete(out, n) || out.CommitConfirmed || !out.RefreshRequired {
		t.Fatalf("original receipt: %+v %v", out, e)
	}
	raw, _ := json.Marshal(out)
	t.Log("original native receipt", string(raw))
	return out
}
func TestNativeDataWriteTypesImmutablePlansOriginalRowsAndReceipts(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 1)
	table := createNativeTable(t, q)
	runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN tags multi")
	runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN bigtags multi64")
	runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN flags bool")
	_, r := nativeData(t, s, table)
	first := insertNativeEdit("18446744073709551615", "猫 original")
	first.Values["qty"] = contract.DataValue{Kind: contract.Integer, Text: "4294967295"}
	first.Values["price"] = contract.DataValue{Kind: contract.Floating, Text: "1.25"}
	first.Values["tag"] = contract.DataValue{Kind: contract.Text, Text: "x\\y'z"}
	first.Values["metadata"] = contract.DataValue{Kind: contract.JSON, Text: `{"x":1}`}
	first.Values["tags"] = contract.DataValue{Kind: contract.Text, Text: "1,2"}
	first.Values["flags"] = contract.DataValue{Kind: contract.Boolean, Text: "true"}
	first.Values["bigtags"] = contract.DataValue{Kind: contract.Text, Text: "-1,9223372036854775807"}
	p := previewNative(t, r, table, first, insertNativeEdit("", "generated"))
	first.Values["title"] = contract.DataValue{Text: "caller changed"}
	preview := p.Snapshot()
	if preview.Items[0].EntityID != "18446744073709551615" || preview.Items[1].EntityID != "" {
		t.Fatal("explicit/generated identity presentation", preview.Items)
	}
	preview.Edits[0].Values["title"] = contract.DataValue{Text: "preview changed"}
	receipt := saveNative(t, r, table, p, 2)
	if receipt.Outcomes[0].GeneratedID != -1 || receipt.Outcomes[1].GeneratedID == 0 {
		t.Fatal("original generated IDs", receipt.Outcomes)
	}
	if _, e := r.SubmitEdits(context.Background(), 69, "Manticore", table, p); e == nil {
		t.Fatal("one-attempt plan replayed")
	}
	page, e := r.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if e != nil || len(page.Rows) != 2 || page.Rows[0][1].Text != "猫 original" {
		t.Fatal(page, e)
	}
	full, e := r.RowReader(context.Background(), 69, "Manticore", table, *page.RowRefs[0])
	if e != nil || full.Values["metadata"].Kind != contract.JSON || full.Values["qty"].Text != "4294967295" || full.Values["flags"].Text != "1" {
		t.Fatal(full, e)
	}
	edit := contract.DataEdit{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"qty": {Kind: contract.Integer, Text: "8"}, "price": {Kind: contract.Floating, Text: "2.5"}, "tag": {Kind: contract.Text, Text: "b"}, "metadata": {Kind: contract.JSON, Text: `{"x":2}`}, "tags": {Kind: contract.Text, Text: "3,4"}, "bigtags": {Kind: contract.Text, Text: "-2,3"}}}
	edit.Values["flags"] = contract.DataValue{Kind: contract.Boolean, Text: "false"}
	p = previewNative(t, r, table, edit)
	saveNative(t, r, table, p, 1)
	page, e = r.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if e != nil || page.Rows[0][3].Value.Text != "8" || page.Rows[0][4].Text != "b" || page.Rows[0][6].Text != "3,4" || page.Rows[0][8].Text != "0" {
		t.Fatal(page, e)
	}
	saveNative(t, r, table, previewNative(t, r, table, contract.DataEdit{Kind: "delete", Original: page.RowSnapshots[0]}), 1)
	read := runNative(t, q, "SELECT id FROM "+table+" WHERE id=-1")
	if len(read.Sets[0].Rows) != 0 {
		t.Fatal("original row not deleted")
	}
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		raw, _ := json.MarshalIndent(receipt, "", "  ")
		if e = os.WriteFile(filepath.Join(dir, "native-insert-receipt.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
func TestNativeDataWriteValidationGrantsLongRowsAndMetadataChanges(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 1)
	table := createNativeTable(t, q)
	_, r := nativeData(t, s, table)
	for _, value := range []contract.DataValue{{Kind: contract.Integer, Text: "4294967296"}, {Null: true}, {Default: true}, {Kind: contract.Floating, Text: "NaN"}} {
		edit := insertNativeEdit("1", "invalid")
		edit.Values["qty"] = value
		if _, e := r.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{edit}); e == nil {
			t.Fatal("invalid value dispatched", value)
		}
	}
	long := strings.Repeat("猫", 2049)
	saveNative(t, r, table, previewNative(t, r, table, insertNativeEdit("1", long)), 1)
	page, e := r.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if e != nil {
		t.Fatal(e)
	}
	full, e := r.RowReader(context.Background(), 69, "Manticore", table, *page.RowRefs[0])
	if e != nil || full.Values["title"].Text != long || !full.PreviewSnapshot.Original[1].Truncated {
		t.Fatal(full, e)
	}
	for _, field := range []string{"id", "title"} {
		edit := contract.DataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{field: {Text: "2"}}}
		if _, e = r.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{edit}); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal(field, e)
		}
	}
	changed := snapshotCopy(page.RowSnapshots[0])
	changed.Original[2].Text = "forged"
	edit := contract.DataEdit{Kind: "update", Original: changed, Values: map[string]contract.DataValue{"qty": {Text: "9"}}}
	if _, e = r.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{edit}); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("forged original", e)
	}
	changed = snapshotCopy(page.RowSnapshots[0])
	*changed.Fingerprints[1] = "forged"
	edit.Original = changed
	if _, e = r.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{edit}); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("forged fingerprint", e)
	}
	_, other := nativeData(t, s, table)
	edit.Original = page.RowSnapshots[0]
	if _, e = other.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{edit}); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("foreign document grant", e)
	}
	p := previewNative(t, r, table, edit)
	runNative(t, q, "UPDATE "+table+" SET qty=3 WHERE id=1")
	out, e := r.SubmitEdits(context.Background(), 69, "Manticore", table, p)
	if !errors.Is(e, contract.ErrDataConflict) || out.Applied != 0 || out.Sequential.Statements[0].State != contract.DataWriteNotSent {
		t.Fatal(out, e)
	}
	if _, e = r.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{insertNativeEdit("2", "blocked")}); !errors.Is(e, contract.ErrDataCommitUncertain) {
		t.Fatal("incomplete write replay", e)
	}
	_, fresh := nativeData(t, s, table)
	p = previewNative(t, fresh, table, insertNativeEdit("2", "metadata"))
	runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN extra integer")
	out, e = fresh.SubmitEdits(context.Background(), 69, "Manticore", table, p)
	if !errors.Is(e, contract.ErrDataConflict) || out.Applied != 0 {
		t.Fatal("changed schema accepted", out, e)
	}
	// Full editing is explicitly limited even when a small scalar is changed.
	if _, err := q.Execute(context.Background(), "Manticore", "INSERT INTO "+table+" (id,title) VALUES (4,'"+strings.Repeat("x", contract.DataEditMaxBytes+1)+"')"); err != nil {
		t.Fatal(err)
	}
	_, bounded := nativeData(t, s, table)
	boundedPage, err := bounded.AdvancedLoader(context.Background(), 69, "Manticore", table, contract.DataOptions{Limit: 1, FilterColumn: "id", FilterMode: "equal", FilterValue: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bounded.RowReader(context.Background(), 69, "Manticore", table, *boundedPage.RowRefs[0]); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("unbounded complete edit", err)
	}
	tooLarge := contract.DataEdit{Kind: "update", Original: boundedPage.RowSnapshots[0], Values: map[string]contract.DataValue{"qty": {Text: "7"}}}
	if _, err = bounded.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{tooLarge}); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("oversize row update", err)
	}
	saveNative(t, bounded, table, previewNative(t, bounded, table, contract.DataEdit{Kind: "delete", Original: boundedPage.RowSnapshots[0]}), 1)
	// A successful refresh revokes old observed row identities.
	_, fresh = nativeData(t, s, table)
	page, e = fresh.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = fresh.Loader(context.Background(), 69, "Manticore", table, 0, 10); e != nil {
		t.Fatal(e)
	}
	if _, e = fresh.RowReader(context.Background(), 69, "Manticore", table, *page.RowRefs[0]); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("old grant accepted", e)
	}
}
func TestNativeDataWriteOriginalFailuresLossCancellationCloseAndNoReplay(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 1)
	table := createNativeTable(t, q)
	cfg, g := gateConfig(t, cfg)
	s := openNative(t, cfg)
	for i, mode := range []string{"failure", "drop", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			doc, r := nativeData(t, s, table)
			start := 100 + i*10
			edits := []contract.DataEdit{insertNativeEdit(fmt.Sprint(start), "prefix"), insertNativeEdit(fmt.Sprint(start+1), "middle"), insertNativeEdit(fmt.Sprint(start+2), "suffix")}
			if mode == "failure" {
				runNative(t, q, fmt.Sprintf("INSERT INTO %s (id,title) VALUES (%d,'exists')", table, start+1))
			}
			p := previewNative(t, r, table, edits...)
			sql := p.(*dataWritePlan).statements[1].SQL
			var observed <-chan []byte
			if mode != "failure" {
				observed = g.arm(sql, mode)
			}
			type response struct {
				r *contract.DataEditResult
				e error
			}
			done := make(chan response, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { v, e := r.SubmitEdits(ctx, 69, "Manticore", table, p); done <- response{v, e} }()
			if observed != nil {
				observedPacket(t, g, observed)
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "close" {
				if e := doc.Close(); e != nil {
					t.Fatal(e)
				}
			}
			var out response
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original executor did not settle")
			}
			expected := contract.DataWriteUnknown
			if mode == "failure" {
				expected = contract.DataWriteFailed
			}
			if out.e == nil || out.r.Applied != 1 || !out.r.Incomplete || out.r.Sequential.Statements[1].State != expected || out.r.Sequential.Statements[2].State != contract.DataWriteNotSent || out.r.CommitConfirmed {
				t.Fatal(out.r, out.e)
			}
			if mode != "failure" && !errors.Is(out.e, contract.ErrDataCommitUncertain) {
				t.Fatal(out.e)
			}
			if progress, ok := contract.SequentialDataWriteProgress(out.r, 3); !ok || progress.Acknowledged != 1 {
				t.Fatal("incoherent original receipt", progress, ok)
			}
			original := out.r.NativeJSON
			disarm(g)
			if mode != "close" {
				if _, e := r.Count(context.Background(), 69, "Manticore", table); e != nil {
					t.Fatal("independent read after write", e)
				}
				if _, e := r.PreviewEdits(context.Background(), 69, "Manticore", table, []contract.DataEdit{insertNativeEdit("999", "replay")}); !errors.Is(e, contract.ErrDataCommitUncertain) {
					t.Fatal(e)
				}
			}
			if original != out.r.NativeJSON {
				t.Fatal("read rewrote receipt")
			}
			observedRows := runNative(t, q, fmt.Sprintf("SELECT id FROM %s WHERE id IN (%d,%d,%d) ORDER BY id ASC", table, start, start+1, start+2))
			if len(observedRows.Sets[0].Rows) != 2 {
				t.Fatal("suffix dispatched or prefix lost", observedRows)
			}
			raw, _ := json.Marshal(out.r)
			t.Log("original stopped batch", string(raw))
		})
	}
}

func TestNativeDataWriteConditionalZeroIsAcknowledgedNotUnknown(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 1)
	table := createNativeTable(t, q)
	runNative(t, q, "INSERT INTO "+table+" (id,title,qty) VALUES (1,'original',7)")
	cfg, g := gateConfig(t, cfg)
	s := openNative(t, cfg)
	_, r := nativeData(t, s, table)
	page, e := r.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if e != nil {
		t.Fatal(e)
	}
	p := previewNative(t, r, table, contract.DataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"qty": {Text: "8"}}})
	var cols []string
	for _, name := range page.Columns {
		cols = append(cols, quote(name))
	}
	source := "SELECT " + strings.Join(cols, ",") + " FROM " + quote("Manticore") + "." + quote(table) + " WHERE id=1 LIMIT 2 OPTION max_matches=2"
	observed := g.arm(source, "hold")
	type response struct {
		r *contract.DataEditResult
		e error
	}
	done := make(chan response, 1)
	go func() { v, e := r.SubmitEdits(context.Background(), 69, "Manticore", table, p); done <- response{v, e} }()
	observedPacket(t, g, observed)
	runNative(t, q, "UPDATE "+table+" SET qty=9 WHERE id=1")
	g.mu.Lock()
	close(g.resume)
	g.mu.Unlock()
	var out response
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("conditional write did not settle")
	}
	if !errors.Is(out.e, contract.ErrDataConflict) || out.r.CommitUncertain || !out.r.Incomplete || out.r.Applied != 1 || out.r.Sequential.Statements[0].State != contract.DataWriteAcknowledged || out.r.Sequential.Statements[0].AffectedRows != 0 {
		t.Fatal(out.r, out.e)
	}
	disarm(g)
	actual := runNative(t, q, "SELECT qty FROM "+table+" WHERE id=1")
	if actual.Sets[0].Rows[0][0].Text != "9" {
		t.Fatal("condition overwrote concurrent scalar attribute")
	}
	t.Log("original acknowledged zero rows retained without transaction or unknown-outcome claim")
}
