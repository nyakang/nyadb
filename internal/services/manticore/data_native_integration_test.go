package manticore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeData(t *testing.T, s *Session, table string) (contract.DataDocumentLease, contract.DataRuntime) {
	t.Helper()
	d, e := s.ReserveDataDocument(s.Namespace("Manticore").Object("TABLE", table))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := d.Close(); e != nil {
			t.Error(e)
		}
	})
	return d, d.Runtime()
}
func TestNativeDataPagesTypedValuesFiltersCompleteExportAndOriginalScope(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 1)
	table := createNativeTable(t, q)
	const ordinary = 2005
	long := strings.Repeat("猫", 2049)
	for start := 1; start <= ordinary; start += 250 {
		var values []string
		for id := start; id < start+250 && id <= ordinary; id++ {
			values = append(values, fmt.Sprintf("(%d,'row %d',1.25,%d,'group %d','{\"n\":%d}')", id, id, id%5, id%2, id))
		}
		r, e := q.Execute(context.Background(), "Manticore", "INSERT INTO "+table+" (id,title,price,qty,tag,metadata) VALUES "+strings.Join(values, ","))
		if e != nil || r.AffectedRows != int64(len(values)) {
			t.Fatal(r, e)
		}
	}
	for _, id := range []string{"9223372036854775808", "18446744073709551615", "9223372036854775807"} {
		r, e := q.Execute(context.Background(), "Manticore", "INSERT INTO "+table+" (id,title,qty,tag,metadata) VALUES ("+id+",'"+long+"',9,'x\\\\y\\'z','{\"wide\":true}')")
		if e != nil || r.AffectedRows != 1 {
			t.Fatal(r, e)
		}
	}
	d, r := nativeData(t, s, table)
	object := s.Namespace("Manticore").Object("TABLE", table)
	if r.Chunk == nil || r.Saver != nil || !r.Capabilities.EditableTables || r.Capabilities.Transactions || r.PreviewEdits == nil || r.SubmitEdits == nil || !r.Capabilities.Count || !r.Capabilities.Export {
		t.Fatal("unverified capability published")
	}
	first, e := r.AdvancedLoader(context.Background(), 69, "Manticore", table, contract.DataOptions{Object: object, Limit: 3})
	if e != nil {
		t.Fatal(e)
	}
	if len(first.Rows) != 3 || !first.HasMore || first.Rows[0][0].Value.Text != "-9223372036854775808" || first.Rows[1][0].Value.Text != "-1" || first.Rows[2][0].Value.Text != "1" {
		t.Fatalf("original signed id ordering %+v", first)
	}
	if first.Editable == nil || !*first.Editable || first.StableOrder || len(first.RowRefs) != 3 || len(first.RowSnapshots) != 3 || first.ColumnMetadata[0].Key != "" {
		t.Fatal("missing original grants or invented primary key")
	}
	if !first.Rows[0][1].Truncated || utf8.RuneCountInString(first.Rows[0][1].Value.Text) != 1024 || first.Rows[0][5].Value.Kind != contract.JSON {
		t.Fatal("typed preview", first.Rows[0])
	}
	if slices.Contains(first.FilterColumns, "title") || slices.Contains(first.FilterColumns, "metadata") || !slices.Contains(first.FilterColumns, "tag") || slices.Contains(first.SortColumns, "title") {
		t.Fatal("native column capabilities", first.FilterColumns, first.SortColumns)
	}
	last, e := r.Loader(context.Background(), 69, "Manticore", table, ordinary+1, 3)
	if e != nil || len(last.Rows) != 2 || last.HasMore || last.Rows[1][0].Value.Text != "9223372036854775807" {
		t.Fatal(last, e)
	}
	count, e := r.Count(context.Background(), 69, "Manticore", table)
	if e != nil || count != ordinary+3 {
		t.Fatal(count, e)
	}
	info, e := r.TableInfo(context.Background(), 69, "Manticore", table)
	if e != nil || info.EstimatedRows == nil || *info.EstimatedRows != count || info.Engine != "rt" {
		t.Fatal(info, e)
	}
	filter := contract.DataOptions{Limit: 10, FilterColumn: "tag", FilterMode: "equal", FilterValue: "x\\y'z"}
	filtered, e := r.AdvancedLoader(context.Background(), 69, "Manticore", table, filter)
	if e != nil || len(filtered.Rows) != 3 || strings.Contains(filtered.QuerySQL, "x\\y") {
		t.Fatal(filtered, e)
	}
	a, b := contract.DataFilterRule{Column: "qty", Mode: "equal", Value: "2"}, contract.DataFilterRule{Column: "qty", Mode: "equal", Value: "4"}
	options := contract.DataOptions{Limit: 7, Condition: &contract.FilterExpression[contract.DataFilterRule]{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{{Op: "rule", Rule: &a}, {Op: "rule", Rule: &b}}}, Sorts: []contract.DataSortRule{{Column: "qty", Descending: true}, {Column: "id", Descending: true}}}
	sorted, e := r.AdvancedLoader(context.Background(), 69, "Manticore", table, options)
	if e != nil || len(sorted.Rows) != 7 || sorted.Rows[0][3].Value.Text != "4" || sorted.Rows[0][0].Value.Text != "2004" {
		t.Fatal(sorted, e)
	}
	for _, bad := range []contract.DataOptions{{Limit: 1, FilterColumn: "title", FilterMode: "equal", FilterValue: "row 1"}, {Limit: 1, FilterColumn: "qty", FilterMode: "is_null"}, {Limit: 1, SortColumn: "metadata"}} {
		if _, e = r.AdvancedLoader(context.Background(), 69, "Manticore", table, bad); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("unsupported native semantics accepted", e)
		}
	}
	if _, e = r.Loader(context.Background(), 70, "Manticore", table, 0, 1); !errors.Is(e, connections.ErrInvalid) {
		t.Fatal("foreign profile", e)
	}
	if _, e = r.Loader(context.Background(), 69, "Manticore", table, 0, 1); e != nil {
		t.Fatal("validation retired undispatched owner", e)
	}
	// Export passes the native default maximum and preserves the complete long
	// original field. Caller mutations after admission cannot alter later pages.
	fields := []string{"id", "title"}
	exportOptions := contract.DataOptions{Sorts: []contract.DataSortRule{{Column: "id"}}}
	exported := 0
	e = r.Exporter(context.Background(), 69, "Manticore", table, exportOptions, fields, func(row []contract.DataValue) error {
		exported++
		expected := ""
		switch exported {
		case 1:
			expected = "-9223372036854775808"
		case 2:
			expected = "-1"
		case ordinary + 3:
			expected = "9223372036854775807"
		default:
			expected = strconv.Itoa(exported - 2)
		}
		if len(row) != 2 || row[0].Text != expected || row[0].Kind != contract.Integer {
			return fmt.Errorf("export ordering at %d: %+v", exported, row)
		}
		if exported == 1 || exported == 2 || exported == ordinary+3 {
			if row[1].Text != long {
				return errors.New("export truncated original field")
			}
		}
		fields[0] = "foreign"
		exportOptions.Sorts[0].Column = "foreign"
		return nil
	})
	if e != nil || exported != ordinary+3 {
		t.Fatal(exported, e)
	}

	rule := contract.DataFilterRule{Column: "qty", Mode: "less", Value: "9"}
	conditioned := contract.DataOptions{Condition: &contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &rule}}
	matches := 0
	e = r.Exporter(context.Background(), 69, "Manticore", table, conditioned, []string{"id"}, func(row []contract.DataValue) error {
		matches++
		if len(row) != 1 || row[0].Text != strconv.Itoa(matches) {
			return errors.New("captured condition changed across export pages")
		}
		rule.Value = "0"
		return nil
	})
	if e != nil || matches != ordinary {
		t.Fatal("original grouped condition export", matches, e)
	}
	var out bytes.Buffer
	e = r.JSONL(context.Background(), object, &out)
	if e != nil {
		t.Fatal(e)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})
	if len(lines) != ordinary+3 {
		t.Fatal("incomplete JSONL", len(lines))
	}
	var original struct {
		Object  contract.ObjectRef
		Columns []contract.StructureColumn
		Values  []contract.Cell
	}
	if e = json.Unmarshal(lines[0], &original); e != nil || original.Object != object || len(original.Columns) != 6 || original.Values[1].Text != long || original.Values[5].Kind != contract.JSON {
		t.Fatal(original, e)
	}
	t.Logf("original count=%d complete export=%d JSONL=%d long field runes=%d; first ids=%s,%s", count, exported, len(lines), utf8.RuneCountInString(original.Values[1].Text), first.Rows[0][0].Value.Text, first.Rows[1][0].Value.Text)
	// Different native table modes must not acquire RT data semantics.
	runNative(t, q, "CREATE TABLE "+table+"_pq (title text) type='pq'")
	_, pq := nativeData(t, s, table+"_pq")
	if _, e = pq.Loader(context.Background(), 69, "Manticore", table+"_pq", 0, 1); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("percolate treated as RT", e)
	}
	runNative(t, q, "DROP TABLE "+table+"_pq")
	if e = d.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Count(context.Background(), 69, "Manticore", table); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("closed document counted", e)
	}
}

func gateConfig(t *testing.T, cfg connections.ManticoreConfig) (connections.ManticoreConfig, *packetGate) {
	t.Helper()
	g := newPacketGate(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	host, port, _ := net.SplitHostPort(g.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	cfg.Host = host
	cfg.Port = uint16(n)
	return cfg, g
}
func observedPacket(t *testing.T, g *packetGate, ch <-chan []byte) {
	t.Helper()
	select {
	case packet := <-ch:
		t.Logf("original held packet=%x", packet)
	case <-time.After(10 * time.Second):
		t.Fatal("original response not observed")
	}
}
func disarm(g *packetGate) { g.mu.Lock(); g.query = ""; g.mu.Unlock() }
func TestNativeDataIndependentCancellationRetiredPagesAndClose(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 1)
	table := createNativeTable(t, q)
	runNative(t, q, "INSERT INTO "+table+" (id,title,qty) VALUES (1,'original',7),(2,'second',8)")
	cfg, g := gateConfig(t, cfg)
	s := openNative(t, cfg)
	doc, r := nativeData(t, s, table)
	m, e := s.TableMetadata(context.Background(), s.Namespace("Manticore").Object("TABLE", table))
	if e != nil {
		t.Fatal(e)
	}
	selectPage, e := selectData(m, contract.DataOptions{}, nil, 0, 3)
	if e != nil {
		t.Fatal(e)
	}
	observed := g.arm(selectPage.sql, "hold")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pageDone := make(chan error, 1)
	go func() { _, e := r.Loader(ctx, 69, "Manticore", table, 0, 2); pageDone <- e }()
	observedPacket(t, g, observed)
	// Count uses its own physical owner even while the page response is parked.
	if n, e := r.Count(context.Background(), 69, "Manticore", table); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	cancel()
	select {
	case e = <-pageDone:
		if !errors.Is(e, contract.ErrSQLSessionLost) {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("page cancellation unacknowledged")
	}
	disarm(g)
	if _, e = r.Loader(context.Background(), 69, "Manticore", table, 0, 2); !errors.Is(e, contract.ErrSQLSessionLost) {
		t.Fatal("implicit page recovery", e)
	}
	if g.calls.Load() != 1 {
		t.Fatal("original page replayed")
	}
	if e = doc.Close(); e != nil {
		t.Fatal(e)
	}
	fresh, rr := nativeData(t, s, table)
	if _, e = rr.Loader(context.Background(), 69, "Manticore", table, 0, 2); e != nil {
		t.Fatal(e)
	}
	countSQL := "SELECT COUNT(*) FROM " + quote("Manticore") + "." + quote(table)
	observed = g.arm(countSQL, "hold")
	countCtx, countCancel := context.WithCancel(context.Background())
	defer countCancel()
	countDone := make(chan error, 1)
	go func() { _, e := rr.Count(countCtx, 69, "Manticore", table); countDone <- e }()
	observedPacket(t, g, observed)
	if _, e = rr.Loader(context.Background(), 69, "Manticore", table, 0, 2); e != nil {
		t.Fatal("count blocked page", e)
	}
	countCancel()
	select {
	case e = <-countDone:
		if !errors.Is(e, contract.ErrSQLSessionLost) {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("count cancellation unacknowledged")
	}
	disarm(g)
	if n, e := rr.Count(context.Background(), 69, "Manticore", table); e != nil || n != 2 {
		t.Fatal("count poisoned independent reads", n, e)
	}
	exportSQL, e := selectData(m, contract.DataOptions{}, nil, 0, exportBatch)
	if e != nil {
		t.Fatal(e)
	}
	observed = g.arm(exportSQL.sql, "hold")
	exportCtx, exportCancel := context.WithCancel(context.Background())
	defer exportCancel()
	exportDone := make(chan error, 1)
	go func() {
		exportDone <- rr.Exporter(exportCtx, 69, "Manticore", table, contract.DataOptions{}, nil, func([]contract.DataValue) error { return nil })
	}()
	observedPacket(t, g, observed)
	exportCancel()
	select {
	case e = <-exportDone:
		if !errors.Is(e, contract.ErrSQLSessionLost) {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("export cancellation unacknowledged")
	}
	disarm(g)
	if _, e = rr.Loader(context.Background(), 69, "Manticore", table, 0, 2); e != nil {
		t.Fatal("export retired page", e)
	}
	// Close cancels a captured count and waits before releasing the document.
	observed = g.arm(countSQL, "hold")
	go func() { _, e := rr.Count(context.Background(), 69, "Manticore", table); countDone <- e }()
	observedPacket(t, g, observed)
	if e = fresh.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-countDone:
		if e == nil {
			t.Fatal("closed read published")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not settle original executor")
	}
	disarm(g)
	if len(s.dataLeases) != 0 || len(s.leases) != 0 || len(s.dataSlots) != 0 || len(s.metadataSlots) != 0 {
		t.Fatal("owners or slots retained")
	}
	if _, e = rr.Loader(context.Background(), 69, "Manticore", table, 0, 2); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
}
