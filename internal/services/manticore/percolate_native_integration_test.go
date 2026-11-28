package manticore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func createNativePercolate(t *testing.T, q contract.QueryLease) string {
	t.Helper()
	name := fmt.Sprintf("nyadb_pq76_%d", time.Now().UnixNano())
	runNative(t, q, "CREATE TABLE "+name+" (title text, tag string) type='pq'")
	t.Cleanup(func() {
		if _, err := q.Execute(context.Background(), "Manticore", "DROP TABLE "+name); err != nil && !errors.Is(err, contract.ErrRevoked) {
			t.Error(err)
		}
	})
	return name
}

func TestNativePercolateOriginalRowsPagingFiltersFieldsAndCompleteExport(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7601)
	table := createNativePercolate(t, q)
	long := " " + strings.Repeat("猫🙂", 1025)
	for start := 1; start <= 2008; start += 250 {
		var values []string
		for id := start; id < start+250 && id <= 2008; id++ {
			tags := fmt.Sprintf("group%d", id%3)
			if id == 1 {
				tags = long
			}
			values = append(values, fmt.Sprintf("(%d,'hello','%s','')", id, tags))
		}
		runNative(t, q, "INSERT INTO "+table+" (id,query,tags,filters) VALUES "+strings.Join(values, ","))
	}
	object := s.Namespace("Manticore").Object("TABLE", table)
	m, err := s.TableMetadata(context.Background(), object)
	if err != nil || m.Engine != "percolate" || len(m.Columns) != 4 || !strings.Contains(m.Definition, "title text") || !strings.Contains(m.Definition, "tag string") {
		t.Fatal(m, err)
	}
	if m.Columns[1].Name != "query" || m.Columns[2].Name != "tags" || m.Columns[3].Name != "filters" {
		t.Fatal("match-input schema replaced original stored-query rows", m.Columns)
	}
	_, runtime := nativeData(t, s, table)
	ctx := context.Background()
	page, err := runtime.Loader(ctx, 69, "Manticore", table, 0, 7)
	if err != nil || len(page.Rows) != 7 || !slices.Equal(page.Columns, []string{"id", "query", "tags", "filters"}) || !page.HasMore || page.Editable == nil || !*page.Editable || !slices.Equal(page.EditKinds, []string{"insert", "delete"}) {
		t.Fatal(page, err)
	}
	if !page.Rows[0][2].Truncated || page.Rows[0][0].Value.Kind != contract.Integer || page.Rows[0][3].Null || page.Rows[0][3].Text != "" {
		t.Fatal("original percolate values changed", page.Rows[0])
	}
	if !slices.Equal(page.FilterColumns, page.Columns) || !slices.Equal(page.SortColumns, page.Columns) {
		t.Fatal(page.FilterColumns, page.SortColumns)
	}
	if _, err = runtime.PreviewEdits(ctx, 69, "Manticore", table, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"tags": {Text: "unsupported"}}}}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("percolate update must never mean replacement", err)
	}

	complete := ""
	for _, offset := range []int64{0, 1024, 2048} {
		fragment := nativeField(t, runtime, table, "tags", *page.RowRefs[0], offset, 1024)
		if fragment.TotalUnits != 2051 || fragment.NextOffset != min(int64(2051), offset+1024) {
			t.Fatal(fragment)
		}
		complete += fragment.Text
	}
	if complete != long {
		t.Fatal("percolate original field changed")
	}
	full, err := runtime.RowReader(ctx, 69, "Manticore", table, *page.RowRefs[0])
	if err != nil || full.Values["tags"].Text != long || full.Values["query"].Text != "hello" {
		t.Fatal(full, err)
	}
	n, err := runtime.Count(ctx, 69, "Manticore", table)
	if err != nil || n != 2008 {
		t.Fatal(n, err)
	}
	info, err := runtime.TableInfo(ctx, 69, "Manticore", table)
	if err != nil || info.Engine != "percolate" || info.EstimatedRows == nil || *info.EstimatedRows != 2008 {
		t.Fatal("native stored_queries count missing", info, err)
	}
	beyond, err := runtime.Loader(ctx, 69, "Manticore", table, 1000, 7)
	if err != nil || len(beyond.Rows) != 7 || beyond.Rows[0][0].Text != "1001" || beyond.Rows[6][0].Text != "1007" {
		t.Fatal(beyond, err)
	}
	a, b := contract.DataFilterRule{Column: "tags", Mode: "equal", Value: "group1"}, contract.DataFilterRule{Column: "tags", Mode: "equal", Value: "group2"}
	filtered, err := runtime.AdvancedLoader(ctx, 69, "Manticore", table, contract.DataOptions{Limit: 7, Condition: &contract.FilterExpression[contract.DataFilterRule]{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{{Op: "rule", Rule: &a}, {Op: "rule", Rule: &b}}}, Sorts: []contract.DataSortRule{{Column: "tags", Descending: true}, {Column: "id"}}})
	if err != nil || len(filtered.Rows) != 7 || filtered.Rows[0][0].Text != "2" || filtered.Rows[6][0].Text != "20" {
		t.Fatal(filtered, err)
	}
	for _, field := range []string{"query", "filters"} {
		value := "hello"
		if field == "filters" {
			value = ""
		}
		v, err := runtime.AdvancedLoader(ctx, 69, "Manticore", table, contract.DataOptions{Limit: 2, FilterColumn: field, FilterMode: "equal", FilterValue: value})
		if err != nil || len(v.Rows) != 2 || v.Rows[0][0].Text != "1" {
			t.Fatal(field, v, err)
		}
	}
	rows := 0
	err = runtime.Exporter(ctx, 69, "Manticore", table, contract.DataOptions{}, []string{"id", "query", "tags", "filters"}, func(row []contract.DataValue) error {
		rows++
		if len(row) != 4 || row[0].Text != fmt.Sprint(rows) || row[1].Text != "hello" || row[3].Null || row[3].Text != "" || rows == 1 && row[2].Text != long {
			return errors.New("original complete percolate export changed")
		}
		return nil
	})
	if err != nil || rows != 2008 {
		t.Fatal(rows, err)
	}
	objectEvidence(t, "percolate-original-metadata.json", m)
	objectEvidence(t, "percolate-original-page.json", page)
	objectEvidence(t, "percolate-beyond-default-page.json", beyond)
	objectEvidence(t, "percolate-original-filtered.json", filtered)
	objectEvidence(t, "percolate-original-info.json", info)
}

func TestNativePercolateFieldCancellationCloseAndRetiredPage(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7604)
	table := createNativePercolate(t, q)
	runNative(t, q, "INSERT INTO "+table+" (id,query,tags) VALUES (1,'hello','original')")
	for _, action := range []string{"cancel", "document-close", "session-close", "page-replacement"} {
		t.Run(action, func(t *testing.T) {
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			d, runtime := nativeData(t, s, table)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			page, err := runtime.Loader(ctx, 69, "Manticore", table, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			source := "SELECT `tags` FROM `Manticore`." + quote(table) + " WHERE id=1 LIMIT 2 OPTION max_matches=2"
			observed := gate.arm(source, "hold")
			type response struct {
				chunk *contract.FieldChunk
				err   error
			}
			done := make(chan response, 1)
			go func() {
				v, e := runtime.Chunk(ctx, 69, "Manticore", table, "tags", *page.RowRefs[0], 0, 1024)
				done <- response{v, e}
			}()
			select {
			case <-observed:
			case <-time.After(10 * time.Second):
				t.Fatal("original field response not observed")
			}
			switch action {
			case "cancel":
				cancel()
			case "document-close":
				if err = d.Close(); err != nil {
					t.Fatal(err)
				}
			case "session-close":
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
			case "page-replacement":
				if _, err = runtime.Loader(context.Background(), 69, "Manticore", table, 0, 1); err != nil {
					t.Fatal(err)
				}
				close(gate.resume)
			}
			select {
			case result := <-done:
				if result.err == nil || result.chunk != nil {
					t.Fatal("revoked field published", result)
				}
				if action == "page-replacement" {
					if !errors.Is(result.err, contract.ErrDataConflict) {
						t.Fatal(result.err)
					}
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original field executor did not settle")
			}
			if gate.calls.Load() != 1 {
				t.Fatal("field replayed", gate.calls.Load())
			}
			if action == "cancel" {
				if n, e := runtime.Count(context.Background(), 69, "Manticore", table); e != nil || n != 1 {
					t.Fatal("field cancellation retired count", n, e)
				}
				if _, e := runtime.Loader(context.Background(), 69, "Manticore", table, 0, 1); e != nil {
					t.Fatal("field cancellation retired page", e)
				}
			}
		})
	}
}
