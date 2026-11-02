package engines

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLiteAdvancedData(t *testing.T) { advancedDataFixture(t, adapterFixture(t), "main") }
func TestPostgreSQL17AdvancedDataFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_advanced_data`)
	advancedDataFixture(t, s, "nyadb_advanced_data")
}
func advancedDataFixture(t *testing.T, s *Session, schema string) {
	ctx := context.Background()
	id := s.id
	r := s.DataRuntime()
	o := s.Namespace(schema).Object("table", "pages")
	binary := "BLOB"
	if s.kind == connections.PostgreSQL {
		binary = "BYTEA"
	}
	run(t, s, "CREATE TABLE "+qualified(o)+"(id BIGINT NOT NULL PRIMARY KEY, bucket INTEGER NOT NULL, note TEXT, payload "+binary+")")
	large := strings.Repeat("数据🙂", 1200)
	for i := int64(1); i <= 9; i++ {
		note := any(large)
		if i == 9 {
			note = nil
		}
		payload := []byte{0, 255}
		if i == 1 {
			payload = []byte{}
		}
		_, err := s.db.Exec("INSERT INTO "+qualified(o)+" VALUES("+s.arg(1)+","+s.arg(2)+","+s.arg(3)+","+s.arg(4)+")", i, i%3, note, payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Run("keyset-and-scope", func(t *testing.T) {
		options := contract.DataOptions{Object: o, Limit: 2, UseKeyset: true, Sorts: []contract.DataSortRule{{Column: "bucket", Descending: true}}}
		var got []string
		for offset := int64(0); ; offset += 2 {
			options.Offset = offset
			p, err := r.AdvancedLoader(ctx, id, schema, o.Name, options)
			if err != nil {
				t.Fatal(err)
			}
			if !p.KeysetAvailable || !p.StableOrder || p.QuerySQL == "" || p.CompletedAt.IsZero() {
				t.Fatal("stable paging unavailable")
			}
			for _, row := range p.Rows {
				got = append(got, row[0].Text)
			}
			if !p.HasMore {
				break
			}
			options.After = p.NextCursor
		}
		if strings.Join(got, ",") != "2,5,8,1,4,7,3,6,9" {
			t.Fatalf("seek lost, repeated or reordered rows: %v", got)
		}
		first, err := r.AdvancedLoader(ctx, id, schema, o.Name, contract.DataOptions{Limit: 2, UseKeyset: true})
		if err != nil {
			t.Fatal(err)
		}
		changed := contract.DataOptions{Limit: 2, UseKeyset: true, After: first.NextCursor, SortColumn: "bucket"}
		if _, err = r.AdvancedLoader(ctx, id, schema, o.Name, changed); err == nil {
			t.Fatal("changed rules accepted old cursor")
		}
		oldGeneration := s.generation
		s.generation++
		_, err = r.AdvancedLoader(ctx, id, schema, o.Name, contract.DataOptions{Limit: 2, UseKeyset: true, After: first.NextCursor})
		s.generation = oldGeneration
		if err == nil {
			t.Fatal("old generation cursor accepted")
		}
		run(t, s, "ALTER TABLE "+qualified(o)+" ADD COLUMN extra TEXT")
		if _, err = r.AdvancedLoader(ctx, id, schema, o.Name, contract.DataOptions{Limit: 2, UseKeyset: true, After: first.NextCursor}); err == nil {
			t.Fatal("changed metadata cursor accepted")
		}
		p, err := r.AdvancedLoader(ctx, id, schema, o.Name, contract.DataOptions{Limit: 2, UseKeyset: true, SortColumn: "note"})
		if err != nil {
			t.Fatal(err)
		}
		if p.KeysetAvailable || p.StableOrder {
			t.Fatal("nullable sort granted seek")
		}
	})
	t.Run("bounded-preview-chunk-and-full-edit", func(t *testing.T) {
		p, err := r.AdvancedLoader(ctx, id, schema, o.Name, contract.DataOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Rows) != 9 || !p.Rows[0][2].Truncated || p.RowSnapshots[0] != nil || p.RowRefs[0] == nil || p.Rows[0][3].Null || p.Rows[0][3].Value.Kind != contract.Binary {
			t.Fatal("preview lost identity, NULL or truncation evidence")
		}
		ref := *p.RowRefs[0]
		chunk, err := r.Chunk(ctx, id, schema, o.Name, "payload", ref, 0, 1024)
		if err != nil || chunk.Null || !chunk.Binary || chunk.Text != "0x" || chunk.NextOffset != 0 {
			t.Fatalf("empty binary chunk changed: %+v %v", chunk, err)
		}
		var text strings.Builder
		offset := int64(0)
		for {
			chunk, err = r.Chunk(ctx, id, schema, o.Name, "note", ref, offset, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if chunk.NextOffset-offset > 1024 || chunk.Binary || chunk.Null {
				t.Fatal("chunk exceeded units or changed type")
			}
			text.WriteString(chunk.Text)
			offset = chunk.NextOffset
			if !chunk.HasMore {
				break
			}
		}
		if text.String() != large || offset != 3600 {
			t.Fatal("Unicode field chunks changed text or offsets")
		}
		row, err := r.RowReader(ctx, id, schema, o.Name, ref)
		if err != nil || row.Values["note"].Text != large || row.Snapshot == nil {
			t.Fatalf("complete explicit record failed: %v", err)
		}
		null, err := r.Chunk(ctx, id, schema, o.Name, "note", *p.RowRefs[8], 0, 1024)
		if err != nil || !null.Null {
			t.Fatalf("NULL chunk changed: %+v %v", null, err)
		}
		foreign := ref
		foreign.Object.ProfileID++
		if _, err = r.Chunk(ctx, id, schema, o.Name, "note", foreign, 0, 1024); err == nil {
			t.Fatal("foreign identity accepted")
		}
		partial := ref
		partial.Columns = []string{"bucket"}
		partial.TypedValues = []contract.Cell{{Kind: contract.Integer, Text: "1"}}
		if _, err = r.RowReader(ctx, id, schema, o.Name, partial); err == nil {
			t.Fatal("partial nonunique identity accepted")
		}
		huge := strings.Repeat("x", (1<<20)+1)
		if _, err = s.db.Exec("UPDATE "+qualified(o)+" SET note="+s.arg(1)+" WHERE id=1", huge); err != nil {
			t.Fatal(err)
		}
		if _, err = r.RowReader(ctx, id, schema, o.Name, ref); err == nil {
			t.Fatal("over-limit field entered editor")
		}
		if _, err = s.db.Exec("UPDATE "+qualified(o)+" SET note="+s.arg(1)+" WHERE id=1", large); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("count-and-transaction", func(t *testing.T) {
		count, err := r.Count(ctx, id, schema, o.Name)
		if err != nil || count != 9 {
			t.Fatalf("exact count failed: %d %v", count, err)
		}
		tx, err := r.Transactions.Begin(ctx, id, schema, o.Name)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Close()
		p, err := r.Transactions.Page(ctx, tx, id, schema, o.Name, contract.DataOptions{Limit: 1, UseKeyset: true})
		if err != nil {
			t.Fatal(err)
		}
		full, err := r.Transactions.Row(ctx, tx, id, schema, o.Name, *p.RowRefs[0])
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Transactions.Save(ctx, tx, id, schema, o.Name, []contract.DataEdit{{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"note": {Kind: contract.Text, Text: "staged"}}}})
		if err != nil {
			t.Fatal(err)
		}
		chunk, err := r.Transactions.Chunk(ctx, tx, id, schema, o.Name, "note", *p.RowRefs[0], 0, 1024)
		if err != nil || chunk.Text != "staged" {
			t.Fatalf("transaction chunk escaped owner: %+v %v", chunk, err)
		}
		if err = r.Transactions.Rollback(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if _, err = r.Transactions.Chunk(ctx, tx, id, schema, o.Name, "note", *p.RowRefs[0], 0, 1024); err == nil {
			t.Fatal("closed transaction read accepted")
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err = r.Count(cancelled, id, schema, o.Name); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel not acknowledged: %v", err)
		}
	})
	t.Run("analysis-precision-and-scope", func(t *testing.T) {
		statsObject := s.Namespace(schema).Object("table", "stats")
		number := "INTEGER"
		one, two := any(int64(9223372036854775805)), any(int64(9223372036854775807))
		wantMean := "9223372036854775806"
		wantSD := "1"
		if s.kind == connections.PostgreSQL {
			number = "NUMERIC(40,20)"
			one, two = "12345678901234567890.12345678901234567890", "12345678901234567890.12345678901234567892"
			wantMean = "12345678901234567890.12345678901234567891"
			wantSD = "0.00000000000000000001"
		}
		run(t, s, "CREATE TABLE "+qualified(statsObject)+"(id INTEGER PRIMARY KEY,n "+number+", label TEXT,payload "+binary+")")
		for i, v := range []any{one, two, nil} {
			if _, err := s.db.Exec("INSERT INTO "+qualified(statsObject)+" VALUES("+s.arg(1)+","+s.arg(2)+","+s.arg(3)+","+s.arg(4)+")", i, v, "frequency", []byte{0, 255}); err != nil {
				t.Fatal(err)
			}
		}
		a, err := r.Analyzer(ctx, id, schema, statsObject.Name, contract.DataOptions{}, []string{"n", "label", "payload"})
		if err != nil {
			t.Fatal(err)
		}
		n := a.Columns[0]
		equalRat := func(got, want string) bool {
			x, xok := new(big.Rat).SetString(got)
			y, yok := new(big.Rat).SetString(want)
			return xok && yok && x.Cmp(y) == 0
		}
		if a.Rows != 3 || n.Numeric != 2 || n.Null != 1 || n.Distinct != 2 || n.Singletons != 2 || !equalRat(n.Mean, wantMean) || !equalRat(n.Median, wantMean) || !equalRat(n.StdDeviation, wantSD) {
			t.Fatalf("precise summary changed: %+v", n)
		}
		if a.Columns[2].Distribution[0].Value != "0x00ff" || a.Columns[1].ModeCount != 1 || a.Columns[1].MaxFrequency != 3 {
			t.Fatal("typed frequency or modes changed")
		}
		filtered, err := r.Analyzer(ctx, id, schema, statsObject.Name, contract.DataOptions{FilterMode: "equal", FilterColumn: "id", FilterValue: "0", SortColumn: "id", SortDescending: true}, []string{"n"})
		if err != nil || filtered.Rows != 1 {
			t.Fatalf("filter not applied to all stats: %v", err)
		}
		if _, err = r.Analyzer(ctx, id, schema, statsObject.Name, contract.DataOptions{Limit: 1}, []string{"n"}); err == nil {
			t.Fatal("page passed as full analysis")
		}
		if s.kind == connections.SQLite {
			run(t, s, "INSERT INTO "+qualified(statsObject)+" VALUES(3,'9223372036854775807','typed text',x'')")
			mixed, err := r.Analyzer(ctx, id, schema, statsObject.Name, contract.DataOptions{}, []string{"n"})
			if err != nil || mixed.Columns[0].Numeric != 3 {
				t.Fatalf("SQLite integer affinity changed: %v %+v", err, mixed)
			}
		}
	})
}

func TestSQLiteSeekUsesBinaryTieBreakerForDeclaredNoCase(t *testing.T) {
	s := adapterFixture(t)
	run(t, s, `CREATE TABLE collated(k TEXT COLLATE NOCASE NOT NULL,v TEXT); CREATE UNIQUE INDEX raw_unique ON collated(k COLLATE BINARY); INSERT INTO collated VALUES('A','first'),('a','second'),('B','third');`)
	r := s.DataRuntime()
	o := contract.DataOptions{Limit: 1, UseKeyset: true, SortColumn: "k"}
	var got []string
	for {
		p, err := r.AdvancedLoader(context.Background(), s.id, "main", "collated", o)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range p.Rows {
			got = append(got, row[0].Text)
		}
		if !p.HasMore {
			break
		}
		o.After = p.NextCursor
		o.Offset++
	}
	if strings.Join(got, ",") != "A,a,B" {
		t.Fatalf("collation tie skipped a unique value: %v", got)
	}
}
func TestPostgreSQL17AdvancedCountCancellationFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE TABLE public.nyadb_count_wait(id INTEGER PRIMARY KEY)`)
	lock, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err = lock.Exec(`LOCK TABLE public.nyadb_count_wait IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err = s.DataRuntime().Count(ctx, s.id, "public", "nyadb_count_wait"); err == nil {
		t.Fatal("blocked count ignored cancellation")
	}
	if err = lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DataRuntime().Count(context.Background(), s.id, "public", "nyadb_count_wait"); err != nil {
		t.Fatalf("cancel poisoned later reads: %v", err)
	}
}

func TestSQLiteCursorRejectsReopenedPhysicalSession(t *testing.T) {
	s := adapterFixture(t)
	p, err := s.DataRuntime().AdvancedLoader(context.Background(), s.id, "main", "records", contract.DataOptions{Limit: 1, UseKeyset: true})
	if err != nil {
		t.Fatal(err)
	}
	path := s.catalog
	s.Close()
	raw, err := OpenSession(context.Background(), connections.Profile{ID: s.id, Kind: connections.SQLite, Database: path}, "")
	if err != nil {
		t.Fatal(err)
	}
	next := raw.(*Session)
	defer next.Close()
	if next.Generation() != s.Generation() {
		t.Fatal("fixture must reproduce repeated backend generation")
	}
	if _, err = next.DataRuntime().AdvancedLoader(context.Background(), s.id, "main", "records", contract.DataOptions{Limit: 1, UseKeyset: true, After: p.NextCursor}); err == nil {
		t.Fatal("reopened session accepted original cursor")
	}
}
func TestSQLiteNulUnicodeAndPastEndChunkStayTyped(t *testing.T) {
	s := adapterFixture(t)
	run(t, s, `CREATE TABLE nul_fields(id INTEGER PRIMARY KEY,note TEXT,payload BLOB)`)
	text := strings.Repeat("🙂", 1100) + "\x00" + "后续"
	if _, err := s.db.Exec(`INSERT INTO nul_fields VALUES(1,?,x'00ff')`, text); err != nil {
		t.Fatal(err)
	}
	r := s.DataRuntime()
	ctx := context.Background()
	page, err := r.AdvancedLoader(ctx, s.id, "main", "nul_fields", contract.DataOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.RowSnapshots[0] != nil || !page.Rows[0][1].Truncated {
		t.Fatal("NUL field lost truncation evidence")
	}
	var result strings.Builder
	offset := int64(0)
	for {
		chunk, err := r.Chunk(ctx, s.id, "main", "nul_fields", "note", *page.RowRefs[0], offset, 1024)
		if err != nil {
			t.Fatal(err)
		}
		result.WriteString(chunk.Text)
		offset = chunk.NextOffset
		if !chunk.HasMore {
			break
		}
	}
	if result.String() != text || offset != 1103 {
		t.Fatalf("NUL text changed: got bytes=%d wanted=%d offset=%d", result.Len(), len(text), offset)
	}
	for _, field := range []string{"note", "payload"} {
		chunk, err := r.Chunk(ctx, s.id, "main", "nul_fields", field, *page.RowRefs[0], 9999, 1024)
		if err != nil || chunk.Null || chunk.HasMore || chunk.NextOffset != 9999 {
			t.Fatalf("past-end chunk changed field: %+v %v", chunk, err)
		}
	}
	full, err := r.RowReader(ctx, s.id, "main", "nul_fields", *page.RowRefs[0])
	if err != nil || full.Values["note"].Text != text {
		t.Fatal("explicit NUL field read changed value", err)
	}
	a, err := r.Analyzer(ctx, s.id, "main", "nul_fields", contract.DataOptions{}, []string{"note"})
	if err != nil || !a.Columns[0].Distribution[0].Truncated {
		t.Fatal("NUL frequency was incorrectly marked complete", err)
	}
}

func TestSQLiteLargeAnalysisReadsAllMatchingRows(t *testing.T) {
	s := adapterFixture(t)
	run(t, s, `CREATE TABLE large_stats(id INTEGER PRIMARY KEY,n INTEGER); WITH RECURSIVE ids(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM ids WHERE n<100000) INSERT INTO large_stats SELECT n,n FROM ids;`)
	ctx := context.Background()
	r := s.DataRuntime()
	page, err := r.AdvancedLoader(ctx, s.id, "main", "large_stats", contract.DataOptions{Limit: 100, UseKeyset: true, Offset: 90000})
	if err != nil || len(page.Rows) != 100 || page.Rows[0][0].Text != "90001" {
		t.Fatal("large offset seed failed", err)
	}
	page, err = r.AdvancedLoader(ctx, s.id, "main", "large_stats", contract.DataOptions{Limit: 100, UseKeyset: true, After: page.NextCursor, Offset: 90100})
	if err != nil || page.Rows[0][0].Text != "90101" {
		t.Fatal("large seek failed", err)
	}
	count, err := r.Count(ctx, s.id, "main", "large_stats")
	if err != nil || count != 100000 {
		t.Fatal("large exact count failed", count, err)
	}
	a, err := r.Analyzer(ctx, s.id, "main", "large_stats", contract.DataOptions{}, []string{"n"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Rows != 100000 || a.Columns[0].Mean != "50000.5" || a.Columns[0].Median != "50000.5" || a.Columns[0].Singletons != 100000 {
		t.Fatalf("analysis used a page or lost exact values: %+v", a.Columns[0])
	}
	filtered, err := r.Analyzer(ctx, s.id, "main", "large_stats", contract.DataOptions{FilterColumn: "id", FilterMode: "greater", FilterValue: "99990"}, []string{"n"})
	if err != nil || filtered.Rows != 10 || filtered.Columns[0].Mean != "99995.5" {
		t.Fatal("large filtered scope failed", err)
	}
}
