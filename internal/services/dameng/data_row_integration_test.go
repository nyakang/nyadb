package dameng

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNativeDamengCompleteRowsPreserveValuesAndSnapshotOwnership(t *testing.T) {
	s, _, o, text, bin := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := contract.ComposeData(s)
	page, err := r.Loader(ctx, s.ProfileID(), o.Schema, o.Name, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if r.RowReader == nil || r.Saver != nil || r.Capabilities.EditableTables {
		t.Fatal("complete row reads changed unimplemented edit capability")
	}
	for i, ref := range page.RowRefs {
		row, err := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *ref)
		if err != nil {
			t.Fatal("native complete row failed", i, err)
		}
		if row.Snapshot == nil || row.Snapshot.Mode != "values" || row.Snapshot.Metadata == "" || !reflect.DeepEqual(row.Snapshot.Ref, *ref) || len(row.Columns) != 11 || len(row.Snapshot.Original) != 11 || len(row.Values) != 11 {
			t.Fatal("native complete row lost qualified typed snapshot", i)
		}
		if row.Values["ID"].Kind != contract.Integer || row.Values["N"].Kind != contract.Decimal || row.Values["N"].Text != "123456789012345678.12345678901234567891" || row.Values["D"].Kind != contract.DateTime || row.Values["G"].Kind != contract.Integer {
			t.Fatal("native complete numeric/temporal/generated types changed")
		}
		for j, v := range row.Snapshot.Original {
			if v.Truncated || row.Snapshot.Columns[j] != row.Columns[j].Name {
				t.Fatal("complete read retained a preview or mismatched field", i, j)
			}
		}
		if i == 0 && (row.Values["C"].Null || row.Values["C"].Text != "" || row.Values["L"].Null || row.Values["L"].Kind != contract.Binary || len(row.Values["L"].Bytes) != 0) {
			t.Fatal("native empty LOB became NULL")
		}
		if i == 1 && (row.Values["C"].Text != text || !bytes.Equal(row.Values["L"].Bytes, bin) || !bytes.Equal(row.Values["B"].Bytes, bin) || !row.Values["Z"].Null) {
			t.Fatal("complete read changed native long LOB/binary/null data")
		}
		if i == 2 && (row.Values["T"].Text != strings.Repeat("文", 1400) || !bytes.Equal(row.Values["B"].Bytes, bin[:512])) {
			t.Fatal("complete multibyte scalar field stayed truncated")
		}
		if i == 3 && (!row.Values["C"].Null || !row.Values["L"].Null || row.Values["L"].Kind != contract.Null) {
			t.Fatal("native NULL LOB became empty")
		}
		value := row.Values["B"]
		row.Snapshot.Original[4].Bytes[0] = 99
		row.Snapshot.Ref.Values[0][0] = 99
		if value.Bytes[0] != 0 || ref.Values[0][0] == 99 {
			t.Fatal("complete snapshot shared row/page byte buffers")
		}
		value.Bytes[0] = 88
		again, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *ref)
		if e != nil || again.Values["B"].Bytes[0] != 0 {
			t.Fatal("returned complete buffers changed native source or another request", e)
		}
	}
	if _, err = r.RowReader(ctx, s.ProfileID()+1, o.Schema, o.Name, *page.RowRefs[0]); err == nil {
		t.Fatal("complete read accepted another profile")
	}
	ref := *page.RowRefs[0]
	ref.Object.Catalog = "foreign"
	if _, err = r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, ref); err == nil {
		t.Fatal("complete read accepted another Catalog")
	}
}

func TestNativeDamengCompleteLOBByteLimitsRejectPartialRecords(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	r := s.DataReads()
	page, err := r.Loader(ctx, s.ProfileID(), o.Schema, o.Name, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("x", contract.DataEditMaxBytes)
	bin := bytes.Repeat([]byte{0x80}, contract.DataEditMaxBytes)
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET C=?,L=? WHERE ID=9007199254740993", text, bin); err != nil {
		t.Fatal("owned large LOB parameter fixture failed", err)
	}
	full, err := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil || len(full.Values["C"].Text) != contract.DataEditMaxBytes || len(full.Values["L"].Bytes) != contract.DataEditMaxBytes || !strings.HasPrefix(full.Values["C"].Text, text) || !bytes.HasPrefix(full.Values["L"].Bytes, bin) {
		t.Fatal("exact 1 MiB complete native LOB was truncated or rejected", err)
	}
	preview, err := r.Loader(ctx, s.ProfileID(), o.Schema, o.Name, 0, 1)
	if err != nil || !preview.Rows[0][5].Truncated || len(preview.Rows[0][5].Text) != 1024 || !preview.Rows[0][6].Truncated || len(preview.Rows[0][6].Value.Bytes) != 1024 {
		t.Fatal("large native LOB page sent complete values instead of bounded previews", err)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET C=C||'x' WHERE ID=9007199254740993"); err != nil {
		t.Fatal(err)
	}
	if partial, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0]); !errors.Is(e, contract.ErrDataEditLimit) || partial != nil {
		t.Fatal("oversized complete LOB leaked partial record", e)
	}
	if chunk, e := r.Chunk(ctx, s.ProfileID(), o.Schema, o.Name, "C", *page.RowRefs[0], contract.DataEditMaxBytes, 1024); e != nil || chunk.Text != "x" || chunk.HasMore {
		t.Fatal("oversized complete field lost bounded explicit reads", e)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET C=EMPTY_CLOB() WHERE ID=9007199254740993"); err != nil {
		t.Fatal(err)
	}
	// Native BLOB does not support string concatenation (-6105). Extend the
	// original locator with the native LOB operation, beyond the bind limit.
	if _, err = admin.ExecContext(ctx, "DECLARE V BLOB; BEGIN SELECT L INTO V FROM "+qualified(o)+" WHERE ID=9007199254740993 FOR UPDATE; SYS.DBMS_LOB.WRITEAPPEND(V,1,HEXTORAW('80')); END;"); err != nil {
		t.Fatal(err)
	}
	if partial, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0]); !errors.Is(e, contract.ErrDataEditLimit) || partial != nil {
		t.Fatal("oversized complete binary LOB leaked partial record", e)
	}
	// A character bound alone is insufficient for UTF-8 editing.
	unicode := strings.Repeat("文", contract.DataEditMaxBytes/3+1)
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET C=?,L=EMPTY_BLOB() WHERE ID=9007199254740993", unicode[:len(unicode)-3]); err != nil {
		t.Fatal("owned multibyte LOB fixture failed", err)
	}
	if full, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0]); e != nil || full.Values["C"].Text != unicode[:len(unicode)-3] {
		t.Fatal("within-bound multibyte complete LOB changed characters", e)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET C=C||'文' WHERE ID=9007199254740993"); err != nil {
		t.Fatal(err)
	}
	if partial, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0]); !errors.Is(e, contract.ErrDataEditLimit) || partial != nil {
		t.Fatal("multibyte complete LOB exceeded byte bound without rejection", e)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET C=EMPTY_CLOB(),L=EMPTY_BLOB() WHERE ID=9007199254740993"); err != nil {
		t.Fatal(err)
	}
	if full, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0]); e != nil || full.Values["C"].Null || full.Values["C"].Text != "" {
		t.Fatal("large-read failure poisoned original native pool", e)
	}
}

type completeFieldHook struct {
	*sql.Conn
	fragments    int
	beforeSecond func()
}

func (q *completeFieldHook) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.Contains(query, "SYS.DBMS_LOB.SUBSTR(") {
		q.fragments++
		if q.fragments == 2 {
			q.beforeSecond()
		}
	}
	return q.Conn.QueryRowContext(ctx, query, args...)
}

func TestNativeDamengCompleteFragmentsBlockConcurrentReplacementAndCancelWithoutPartialRow(t *testing.T) {
	s, admin, o, text, bin := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := s.DataReads()
	page, err := r.Loader(ctx, s.ProfileID(), o.Schema, o.Name, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	ref := *page.RowRefs[1]
	changed := strings.Repeat("新", 4097)
	changedBin := bytes.Repeat([]byte{0x42}, len(bin))
	err = s.completeDataRead(ctx, o, func(ctx context.Context, q *sql.Conn, tstruct *contract.TableStructure) error {
		key, e := primaryKeyAt(ctx, q, tstruct)
		if e != nil {
			return e
		}
		var updateErr error
		hook := &completeFieldHook{Conn: q, beforeSecond: func() {
			short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
			defer stop()
			_, updateErr = admin.ExecContext(short, "UPDATE "+qualified(o)+" SET C=?,L=? WHERE ID=?", changed, changedBin, int64(9007199254740994))
		}}
		row, e := readFullRowAt(ctx, hook, tstruct, key, ref)
		if e != nil {
			return e
		}
		if updateErr == nil || hook.fragments < 3 || row[5].Text != text || !bytes.Equal(row[6].Bytes, bin) {
			t.Fatal("complete native field fragments allowed a concurrent replacement", updateErr)
		}
		t.Log("concurrent native LOB replacement rejected during complete read", updateErr)
		return nil
	})
	if err != nil {
		t.Fatal("native complete fragmented snapshot failed", err)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET C=?,L=? WHERE ID=?", changed, changedBin, int64(9007199254740994)); err != nil {
		t.Fatal("same native LOB replacement remained blocked after original read ended", err)
	}
	if row, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, ref); e != nil || row.Values["C"].Text != changed || !bytes.Equal(row.Values["L"].Bytes, changedBin) {
		t.Fatal("later explicit complete read did not see independent commit", e)
	}
	err = s.completeDataRead(ctx, o, func(ctx context.Context, q *sql.Conn, tstruct *contract.TableStructure) error {
		key, e := primaryKeyAt(ctx, q, tstruct)
		if e != nil {
			return e
		}
		short, stop := context.WithCancel(ctx)
		defer stop()
		hook := &completeFieldHook{Conn: q, beforeSecond: stop}
		partial, e := readFullRowAt(short, hook, tstruct, key, ref)
		if !errors.Is(e, context.Canceled) || partial != nil || hook.fragments != 2 {
			t.Fatal("canceled complete field leaked a partial row", e)
		}
		return e
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("fragment cancellation was not preserved after original cleanup", err)
	}
	if row, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, ref); e != nil || row.Values["C"].Text != changed {
		t.Fatal("canceled complete read blocked later explicit request", e)
	}
	if _, err = admin.ExecContext(ctx, "DELETE FROM "+qualified(o)+" WHERE ID=?", int64(9007199254740994)); err != nil {
		t.Fatal(err)
	}
	if partial, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, ref); !errors.Is(e, contract.ErrDataConflict) || partial != nil {
		t.Fatal("missing original row became a complete snapshot", e)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if partial, e := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, ref); !errors.Is(e, contract.ErrRevoked) || partial != nil {
		t.Fatal("closed original complete reader accepted a new request", e)
	}
}
