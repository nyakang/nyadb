package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func requireNativePlain(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_MANTICORE_PLAIN_IT") != "1" {
		t.Skip("requires owned indexer-built 2008-row plain fixture")
	}
}
func TestNativePlainMetadataPagesExportAndConditionalUpdates(t *testing.T) {
	requireNativePlain(t)
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7401)
	ctx := context.Background()
	table := "nyadb_plain"
	object := s.Namespace("Manticore").Object("TABLE", table)
	meta, e := s.TableMetadata(ctx, object)
	if e != nil || meta.Engine != "local" || len(meta.Columns) != 3 || meta.Definition != "" || len(meta.Definitions) != 2 {
		t.Fatal(meta, e)
	}
	var definition contract.NativeMetadataObservation
	if e = json.Unmarshal([]byte(meta.Definitions[0].SQL), &definition); e != nil || definition.ErrorCode != 1146 || definition.SQLState != "42S02" || definition.Error != "table 'nyadb_plain' is not real-time or percolate" {
		t.Fatal(definition, e)
	}
	if _, e = s.TableMetadata(ctx, s.Namespace("Manticore").Object("TABLE", "absent_original")); e == nil {
		t.Fatal("missing table became missing DDL")
	}
	_, r := nativeData(t, s, table)
	page, e := r.Loader(ctx, 69, "Manticore", table, 0, 7)
	if e != nil || len(page.Rows) != 7 || !page.HasMore || !slices.Equal(page.EditKinds, []string{"update"}) || contract.DataColumnEditAllowed(page, "id", "update") || contract.DataColumnEditAllowed(page, "title", "update") || !contract.DataColumnEditAllowed(page, "qty", "update") {
		t.Fatal(page, e)
	}
	if !page.Rows[0][1].Truncated || len([]rune(page.Rows[0][1].Text)) != 1024 || page.Rows[0][0].Value.Kind != contract.Integer {
		t.Fatal("plain preview changed original types/limit", page.Rows[0])
	}
	full, e := r.RowReader(ctx, 69, "Manticore", table, *page.RowRefs[0])
	if e != nil || full.Values["title"].Text != " "+strings.Repeat("猫", 2049) {
		t.Fatal(full, e)
	}
	for _, edit := range []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"qty": {Text: "3"}}}, {Kind: "delete", Original: full.Snapshot}, {Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"title": {Text: "forbidden"}}}} {
		if _, e = r.PreviewEdits(ctx, 69, "Manticore", table, []contract.DataEdit{edit}); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("plain unsupported mutation admitted", e)
		}
	}
	edit := contract.DataEdit{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"qty": {Kind: contract.Integer, Text: "17"}}}
	p := previewNative(t, r, table, edit)
	defer runNative(t, q, "UPDATE nyadb_plain SET qty=1 WHERE id=1")
	receipt := saveNative(t, r, table, p, 1)
	objectEvidence(t, "plain-original-update.json", receipt)
	actual := runNative(t, q, "SELECT qty FROM nyadb_plain WHERE id=1")
	if actual.Rows[0][0] != "17" {
		t.Fatal(actual)
	}
	page, e = r.Loader(ctx, 69, "Manticore", table, 0, 7)
	if e != nil || page.Rows[0][2].Text != "17" {
		t.Fatal(page, e)
	}
	// Updating a plain attribute changes secondary-index availability on the
	// newer server. That volatile observation must not invalidate stable grants.
	meta, e = s.TableMetadata(ctx, object)
	if e != nil {
		t.Fatal(e)
	}
	full, e = r.RowReader(ctx, 69, "Manticore", table, *page.RowRefs[0])
	if e != nil {
		t.Fatal("index status retired original grant", e)
	}
	edit.Original = full.Snapshot
	edit.Values["qty"] = contract.DataValue{Text: "18"}
	p = previewNative(t, r, table, edit)
	saveNative(t, r, table, p, 1)
	if n, e := r.Count(ctx, 69, "Manticore", table); e != nil || n != 2008 {
		t.Fatal(n, e)
	}
	info, e := r.TableInfo(ctx, 69, "Manticore", table)
	if e != nil || info.Engine != "local" || info.EstimatedRows == nil || *info.EstimatedRows != 2008 {
		t.Fatal(info, e)
	}
	beyond, e := r.Loader(ctx, 69, "Manticore", table, 1000, 7)
	if e != nil || len(beyond.Rows) != 7 || beyond.Rows[0][0].Text != "1001" {
		t.Fatal(beyond, e)
	}
	a, b := contract.DataFilterRule{Column: "qty", Mode: "equal", Value: "1"}, contract.DataFilterRule{Column: "qty", Mode: "equal", Value: "2"}
	filtered, e := r.AdvancedLoader(ctx, 69, "Manticore", table, contract.DataOptions{Limit: 7, Condition: &contract.FilterExpression[contract.DataFilterRule]{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{{Op: "rule", Rule: &a}, {Op: "rule", Rule: &b}}}, Sorts: []contract.DataSortRule{{Column: "qty", Descending: true}, {Column: "id"}}})
	if e != nil || filtered.Rows[0][0].Text != "2" || filtered.Rows[6][0].Text != "32" {
		t.Fatal(filtered, e)
	}
	exported := 0
	e = r.Exporter(ctx, 69, "Manticore", table, contract.DataOptions{}, []string{"id", "title"}, func(row []contract.DataValue) error {
		exported++
		if exported == 1 && row[1].Text != " "+strings.Repeat("猫", 2049) {
			return errors.New("original plain title truncated")
		}
		return nil
	})
	if e != nil || exported != 2008 {
		t.Fatal(exported, e)
	}
	objectEvidence(t, "plain-metadata.json", meta)
	objectEvidence(t, "plain-page.json", page)
	objectEvidence(t, "plain-beyond-default-page.json", beyond)
	objectEvidence(t, "plain-filtered-page.json", filtered)
}
func TestNativePlainUnknownUpdateRetainsOriginalReceiptAndBlocksReplay(t *testing.T) {
	requireNativePlain(t)
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7402)
	proxied, gate := gateConfig(t, cfg)
	s := openNative(t, proxied)
	_, r := nativeData(t, s, "nyadb_plain")
	ctx := context.Background()
	page, e := r.Loader(ctx, 69, "Manticore", "nyadb_plain", 99, 1)
	if e != nil || page.Rows[0][0].Text != "100" {
		t.Fatal(page, e)
	}
	edit := contract.DataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"qty": {Text: "27"}}}
	p := previewNative(t, r, "nyadb_plain", edit)
	sql := p.(*dataWritePlan).statements[0].SQL
	observed := gate.arm(sql, "hold")
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	type response struct {
		r *contract.DataEditResult
		e error
	}
	done := make(chan response, 1)
	go func() { v, e := r.SubmitEdits(child, 69, "Manticore", "nyadb_plain", p); done <- response{v, e} }()
	select {
	case ack := <-observed:
		if len(ack) < 6 || ack[4] != 0 {
			t.Fatal("missing original OK")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no original response")
	}
	defer runNative(t, q, "UPDATE nyadb_plain SET qty=0 WHERE id=100")
	cancel()
	var result response
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("original executor did not settle")
	}
	if !errors.Is(result.e, contract.ErrDataCommitUncertain) || result.r == nil || !result.r.CommitUncertain || result.r.Sequential.Statements[0].State != contract.DataWriteUnknown {
		t.Fatal(result)
	}
	actual := runNative(t, q, "SELECT qty FROM nyadb_plain WHERE id=100")
	if actual.Rows[0][0] != "27" {
		t.Fatal(actual)
	}
	if _, e = r.PreviewEdits(ctx, 69, "Manticore", "nyadb_plain", []contract.DataEdit{edit}); !errors.Is(e, contract.ErrDataCommitUncertain) {
		t.Fatal("plain unknown replay admitted", e)
	}
	objectEvidence(t, "plain-unknown-update.json", result.r)
}
