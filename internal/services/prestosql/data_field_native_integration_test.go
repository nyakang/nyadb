package prestosql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestPrestoSQLPrivateReadNativeFieldGrantsAndChangedRows(t *testing.T) {
	f, p, c, _ := nativeProvider(t)
	s, q, _ := nativeDataSession(t, p, c)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	const name = "field66"
	queryNative(t, q, "", `CREATE TABLE memory.default.field66 AS SELECT BIGINT '1' AS id,rpad('',2049,'猫')||'🙂' AS note,concat(X'00ff80',to_utf8(rpad('',1025,'x'))) AS payload,CAST(NULL AS VARCHAR) AS missing,'' AS empty`)
	l, r, label := nativeDataDocument(t, s, name)
	page, err := r.Loader(ctx, 65, label, name, 0, 10)
	if err != nil || len(page.Rows) != 1 || len(page.RowRefs) != 1 {
		t.Fatal("captured original page", err)
	}
	ref := *page.RowRefs[0]
	var chunks []*contract.FieldChunk
	for _, part := range []struct {
		field              string
		offset             int64
		limit              int
		want               string
		total, next        int64
		more, binary, null bool
	}{
		{"note", 0, 1024, strings.Repeat("猫", 1024), 2050, 1024, true, false, false},
		{"note", 2048, 1024, "猫🙂", 2050, 2050, false, false, false},
		{"payload", 0, 3, "0x00ff80", 1028, 3, true, true, false},
		{"payload", 1024, 1024, "0x78787878", 1028, 1028, false, true, false},
		{"missing", 0, 1024, "", 0, 0, false, false, true},
		{"empty", 0, 1024, "", 0, 0, false, false, false},
	} {
		got, err := r.Chunk(ctx, 65, label, name, part.field, ref, part.offset, part.limit)
		if err != nil || got == nil || got.Text != part.want || got.TotalUnits != part.total || got.NextOffset != part.next || got.HasMore != part.more || got.Binary != part.binary || got.Null != part.null {
			t.Fatal("original bounded field", part.field, part.offset, err, got)
		}
		chunks = append(chunks, got)
	}
	_, other, _ := nativeDataDocument(t, s, name)
	if _, err = other.Chunk(ctx, 65, label, name, "note", ref, 0, 1024); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("foreign document accepted read grant", err)
	}
	foreign := ref
	foreign.Object.ProfileID = 66
	if _, err = r.Chunk(ctx, 65, label, name, "note", foreign, 0, 1024); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign original identity accepted", err)
	}
	// Duplicate complete values cannot establish a unique row for a later read.
	queryNative(t, q, "", `INSERT INTO memory.default.field66 SELECT * FROM memory.default.field66`)
	if _, err = r.Chunk(ctx, 65, label, name, "note", ref, 0, 1024); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("ambiguous duplicate row accepted", err)
	}
	queryNative(t, q, "", `CREATE TABLE memory.default.field_copy66 AS SELECT DISTINCT id,CAST('changed' AS VARCHAR) AS note,payload,missing,empty FROM memory.default.field66; DROP TABLE memory.default.field66; CREATE TABLE memory.default.field66 AS SELECT * FROM memory.default.field_copy66; DROP TABLE memory.default.field_copy66`)
	if _, err = r.Chunk(ctx, 65, label, name, "note", ref, 0, 1024); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed row returned later field value", err)
	}
	fresh, err := r.Loader(ctx, 65, label, name, 0, 10)
	if err != nil || fresh.Rows[0][1].Text != "changed" {
		t.Fatal("explicit page refresh", err)
	}
	if _, err = r.Chunk(ctx, 65, label, name, "note", ref, 0, 1024); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("stale page grant survived replacement", err)
	}
	current, err := r.Chunk(ctx, 65, label, name, "note", *fresh.RowRefs[0], 0, 1024)
	if err != nil || current.Text != "changed" {
		t.Fatal("fresh explicit observation", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if len(l.grants) != 0 {
		t.Fatal("closed document retained read grants")
	}
	if _, err = r.Chunk(ctx, 65, label, name, "note", *fresh.RowRefs[0], 0, 1024); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original field owner recovered", err)
	}
	providerProof(t, f, "data-field-grants.json", map[string]any{"original_page": page, "chunks": chunks, "fresh_page": fresh, "fresh_chunk": current, "foreign_and_stale_grants_refused": true, "duplicate_and_changed_rows_refused": true, "closed_grants_released": true})
	queryNative(t, q, "", `DROP TABLE memory.default.field66`)
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}
