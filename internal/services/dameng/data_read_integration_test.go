package dameng

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func nativeReadTable(t *testing.T) (*Session, *sql.DB, contract.ObjectRef, string, []byte) {
	t.Helper()
	c, password := nativeFixture(t)
	admin := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := "NYADB_READ_" + nativeSuffix(t) + ` 中文 "';-- `
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quote(schema)+" AUTHORIZATION SYSDBA"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP SCHEMA "+quote(schema)+" CASCADE") })
	c.Options.DefaultSchema, c.Options.MaxConnections = schema, 1
	s := fixtureSession(t, c, password)
	o := s.Namespace(schema).Object("table", `Data "`)
	q := qualified(o)
	if _, err := admin.ExecContext(ctx, "CREATE TABLE "+q+`(ID BIGINT PRIMARY KEY,GRP INT NOT NULL,N DECIMAL(38,20),T VARCHAR(8000),B VARBINARY(8000),C CLOB,L BLOB,Z VARCHAR(64),E VARBINARY(64),G BIGINT GENERATED ALWAYS AS (ID+1) VIRTUAL,D TIMESTAMP(6))`); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("中文🙂", 1400) + " END "
	bin := bytes.Repeat([]byte{0, 0x80, 0xff, 0x27}, 1400)
	for i := int64(0); i < 4; i++ {
		var short any = ""
		var scalar any = "中文 ' literal %_"
		var scalarBytes any = bin
		if i == 1 {
			short = nil
		}
		if i == 2 {
			scalar = strings.Repeat("文", 1400)
			scalarBytes = bin[:512]
		}
		lobExpr := "?,?"
		args := []any{int64(9007199254740993) + i, i % 2, dpi.Decimal("123456789012345678.12345678901234567891"), scalar, scalarBytes}
		if i == 0 {
			lobExpr = "EMPTY_CLOB(),EMPTY_BLOB()"
		} else if i == 3 {
			lobExpr = "NULL,NULL"
		} else {
			args = append(args, text, bin)
		}
		args = append(args, short, []byte{}, "2026-11-07 12:34:56.123456")
		_, err := admin.ExecContext(ctx, "INSERT INTO "+q+"(ID,GRP,N,T,B,C,L,Z,E,D) VALUES(?,?,?,?,?,"+lobExpr+",?,?,?)", args...)
		if err != nil {
			t.Fatal("native read fixture insert failed at row", i, err)
		}
	}
	return s, admin, o, text, bin
}

func cloneCursor(c *contract.DataCursor) *contract.DataCursor {
	b, _ := json.Marshal(c)
	var out contract.DataCursor
	_ = json.Unmarshal(b, &out)
	return &out
}

func TestNativeDamengDataReadTypedPagingAndBoundedFields(t *testing.T) {
	s, admin, o, text, bin := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r := contract.ComposeData(s)
	if r.AdvancedLoader == nil || r.Count == nil || r.Chunk == nil || r.Saver != nil || r.RowReader == nil || r.Transactions.Begin != nil || r.Importer != nil || r.Exporter != nil || r.Capabilities.EditableTables {
		t.Fatal("native read foundation advertised unimplemented writes")
	}
	opt := contract.DataOptions{Object: o, Limit: 2, UseKeyset: true, Sorts: []contract.DataSortRule{{Column: "GRP", Descending: true}}}
	p, err := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, opt)
	if err != nil {
		t.Fatal("native bounded page failed", err)
	}
	if p.Object != o || len(p.Rows) != 2 || !p.HasMore || !p.StableOrder || !p.KeysetAvailable || p.NextCursor == nil || p.Editable == nil || *p.Editable || len(p.ColumnMetadata) != 11 {
		t.Fatal("native read page lost identity/order/bounds", p)
	}
	row := p.Rows[0]
	if row[0].Value.Kind != contract.Integer || row[0].Text != "9007199254740994" || row[2].Value.Kind != contract.Decimal || row[2].Text != "123456789012345678.12345678901234567891" || row[4].Value.Kind != contract.Binary || !bytes.Equal(row[4].Value.Bytes, bin[:1024]) || !row[4].Truncated || !row[5].Truncated || utf8.RuneCountInString(row[5].Text) != 1024 || !row[7].Null || row[8].Null || row[8].Value.Kind != contract.Binary || len(row[8].Value.Bytes) != 0 {
		t.Fatal("native exact values/null/empty/LOB preview failed", row)
	}
	if p.ColumnMetadata[9].Extra != "GENERATED" || row[9].Text != "9007199254740995" || !strings.Contains(row[10].Text, ".123456") {
		t.Fatal("native generated or temporal read failed")
	}
	if p.RowRefs[0] == nil || p.RowRefs[0].Object != o || p.RowRefs[0].TypedValues[0].Text != row[0].Text {
		t.Fatal("native reliable rowref missing")
	}
	secondOpt := opt
	secondOpt.After = p.NextCursor
	second, err := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, secondOpt)
	if err != nil || len(second.Rows) != 2 || second.HasMore || second.Rows[0][0].Text != "9007199254740993" || second.Rows[1][0].Text != "9007199254740995" {
		t.Fatal("native numeric seek skipped or repeated rows", err, second)
	}
	if !p.Rows[1][5].Null || !p.Rows[1][6].Null || second.Rows[0][5].Null || second.Rows[0][5].Text != "" || second.Rows[0][6].Null || len(second.Rows[0][6].Value.Bytes) != 0 || !second.Rows[1][3].Truncated || utf8.RuneCountInString(second.Rows[1][3].Text) != 1024 {
		t.Fatal("native LOB null/empty or multibyte scalar sentinel failed")
	}
	for _, field := range []string{"B", "C", "L", "T"} {
		var joined string
		var offset int64
		for {
			chunk, e := r.Chunk(ctx, s.ProfileID(), o.Schema, o.Name, field, *p.RowRefs[0], offset, 1024)
			if e != nil {
				t.Fatal("native explicit field chunk failed", field, offset, e)
			}
			if chunk.Offset != offset || chunk.Null || chunk.NextOffset <= offset {
				t.Fatal("native field chunk lost progress", field, chunk)
			}
			joined += chunk.Text
			offset = chunk.NextOffset
			if !chunk.HasMore {
				break
			}
		}
		want := row[3].Text
		if field == "C" {
			want = text
		}
		if field == "B" || field == "L" {
			want = strings.ToUpper(fmtHex(bin))
		}
		if joined != want {
			t.Fatal("native field chunks changed bytes/characters", field)
		}
		end, e := r.Chunk(ctx, s.ProfileID(), o.Schema, o.Name, field, *p.RowRefs[0], offset+5, 64)
		if e != nil || end.Null || end.HasMore || end.Text != "" {
			t.Fatal("slice past EOF became field NULL", field, e, end)
		}
	}
	null, e := r.Chunk(ctx, s.ProfileID(), o.Schema, o.Name, "Z", *p.RowRefs[0], 0, 32)
	if e != nil || !null.Null {
		t.Fatal("NULL field slice became empty", e, null)
	}
	empty, e := r.Chunk(ctx, s.ProfileID(), o.Schema, o.Name, "E", *p.RowRefs[0], 0, 32)
	if e != nil || empty.Null || empty.Text != "" || empty.TotalUnits != 0 {
		t.Fatal("empty binary slice became NULL", e, empty)
	}
	count, e := r.Count(ctx, s.ProfileID(), o.Schema, o.Name)
	if e != nil || count != 4 {
		t.Fatal("native explicit exact count failed", e, count)
	}
	filter := contract.DataOptions{Limit: 10, Condition: &contract.FilterExpression[contract.DataFilterRule]{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{*contract.AndFilters([]contract.DataFilterRule{{Column: "ID", Mode: "equal", Value: "9007199254740993"}}), *contract.AndFilters([]contract.DataFilterRule{{Column: "Z", Mode: "is_null"}})}}}
	filtered, e := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, filter)
	if e != nil || len(filtered.Rows) != 2 || !strings.Contains(filtered.QuerySQL, " OR ") || strings.Contains(filtered.QuerySQL, "9007199254740993") {
		t.Fatal("grouped filter lost parameterization", e, filtered)
	}
	filter = contract.DataOptions{Limit: 10, Filters: []contract.DataFilterRule{{Column: "T", Mode: "equal", Value: row[3].Text}, {Column: "N", Mode: "equal", Value: row[2].Text}}}
	filtered, e = r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, filter)
	if e != nil || len(filtered.Rows) != 3 || strings.Contains(filtered.QuerySQL, row[3].Text) {
		t.Fatal("exact Decimal/text bound filter failed", e)
	}
	if _, e = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET T='after chunk' WHERE ID=9007199254740994"); e != nil {
		t.Fatal("read transaction left a native lock behind", e)
	}
}

func fmtHex(b []byte) string {
	const digits = "0123456789abcdef"
	var out strings.Builder
	for _, v := range b {
		out.WriteByte(digits[v>>4])
		out.WriteByte(digits[v&15])
	}
	return out.String()
}

func TestNativeDamengDataReadRejectsStaleCursorAndUnprovenKeys(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r := s.DataReads()
	opt := contract.DataOptions{Limit: 1, UseKeyset: true}
	p, err := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, opt)
	if err != nil || p.NextCursor == nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*contract.DataCursor){
		"scope":     func(c *contract.DataCursor) { c.RuleSignature = "foreign" },
		"table":     func(c *contract.DataCursor) { c.Table = "other" },
		"direction": func(c *contract.DataCursor) { c.Descending[0] = true },
		"NULL":      func(c *contract.DataCursor) { c.Values[0], _ = json.Marshal(contract.Cell{Kind: contract.Null}) },
		"rounding": func(c *contract.DataCursor) {
			c.Values[0], _ = json.Marshal(contract.Cell{Kind: contract.Integer, Text: "9007199254740993.0"})
		},
		"oversize": func(c *contract.DataCursor) { c.Values[0] = bytes.Repeat([]byte("x"), 8193) },
	} {
		t.Run(name, func(t *testing.T) {
			cursor := cloneCursor(p.NextCursor)
			change(cursor)
			request := opt
			request.After = cursor
			if _, e := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, request); e == nil {
				t.Fatal("untrusted seek cursor accepted")
			}
		})
	}
	changed := opt
	changed.After = p.NextCursor
	changed.Filters = []contract.DataFilterRule{{Column: "ID", Mode: "greater", Value: "0"}}
	if _, e := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, changed); e == nil {
		t.Fatal("cursor accepted after filter change")
	}
	c, password := nativeFixture(t)
	c.Options.DefaultSchema = o.Schema
	other := fixtureSession(t, c, password)
	if _, e := other.DataReads().AdvancedLoader(ctx, other.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1, UseKeyset: true, After: p.NextCursor}); e == nil {
		t.Fatal("cursor escaped original physical session scope")
	}
	if _, e := r.AdvancedLoader(ctx, s.ProfileID()+1, o.Schema, o.Name, opt); e == nil {
		t.Fatal("foreign profile read accepted")
	}
	foreign := opt
	foreign.Object = o
	foreign.Object.Catalog = "foreign"
	if _, e := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, foreign); e == nil {
		t.Fatal("foreign Catalog read accepted")
	}
	if _, e := admin.ExecContext(ctx, "ALTER TABLE "+qualified(o)+" ADD NEWCOL INT"); e != nil {
		t.Fatal(e)
	}
	changed = opt
	changed.After = p.NextCursor
	if _, e := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, o.Name, changed); e == nil {
		t.Fatal("cursor survived native structure change")
	}
	for _, ddl := range []string{`"NoKey"(ID BIGINT,T VARCHAR(32))`, `"NullableUnique"(ID BIGINT UNIQUE,T VARCHAR(32))`, `"TextKey"(ID VARCHAR(32) PRIMARY KEY,T VARCHAR(32))`} {
		if _, e := admin.ExecContext(ctx, "CREATE TABLE "+quote(o.Schema)+"."+ddl); e != nil {
			t.Fatal(e)
		}
	}
	for _, name := range []string{"NoKey", "NullableUnique", "TextKey"} {
		if _, e := admin.ExecContext(ctx, "INSERT INTO "+quote(o.Schema)+"."+quote(name)+" VALUES(1,'x')"); e != nil {
			t.Fatal(e)
		}
		page, e := r.AdvancedLoader(ctx, s.ProfileID(), o.Schema, name, opt)
		if e != nil || page.StableOrder || page.KeysetAvailable || page.NextCursor != nil || len(page.RowRefs) != 1 || page.RowRefs[0] != nil {
			t.Fatal("unverified unique semantics claimed", name, e, page)
		}
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Count(ctx, s.ProfileID(), o.Schema, o.Name); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("captured read callback survived original Close", e)
	}
}

func TestNativeDamengDataReadSnapshotDDLAndCancellation(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := s.dataRead(ctx, o, func(ctx context.Context, q *sql.Conn, _ *contract.TableStructure) error {
		var before, after string
		if e := q.QueryRowContext(ctx, "SELECT T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&before); e != nil {
			return e
		}
		if _, e := admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET T='concurrent' WHERE ID=9007199254740993"); e != nil {
			return e
		}
		if e := q.QueryRowContext(ctx, "SELECT T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&after); e != nil {
			return e
		}
		if before != after {
			t.Error("native read snapshot changed after independent committed update")
		}
		short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		defer stop()
		_, e := admin.ExecContext(short, "ALTER TABLE "+qualified(o)+" ADD DDLWAIT INT")
		if e == nil {
			t.Error("native ROW SHARE allowed concurrent DDL")
		} else {
			t.Log("concurrent native DDL rejected", e)
		}
		var added int
		if e = admin.QueryRowContext(ctx, `SELECT COUNT(*) FROM SYS.ALL_TAB_COLS WHERE OWNER=? AND TABLE_NAME=? AND COLUMN_NAME='DDLWAIT'`, o.Schema, o.Name).Scan(&added); e != nil || added != 0 {
			t.Error("rejected DDL changed native structure", e, added)
		}
		return nil
	})
	if err != nil {
		t.Fatal("native snapshot/lock read failed", err)
	}
	// The exact same DDL must succeed after the original read releases its lock.
	// This rules out interpreting an unrelated syntax/feature rejection as a lock.
	if _, err = admin.ExecContext(ctx, "ALTER TABLE "+qualified(o)+" ADD DDLWAIT INT"); err != nil {
		t.Fatal("native DDL remained rejected after original read ended", err)
	}
	r := s.DataReads()
	count, err := r.Count(ctx, s.ProfileID(), o.Schema, o.Name)
	if err != nil || count != 4 {
		t.Fatal("read completion did not release original native locks", err)
	}
	lock, err := admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err = lock.ExecContext(ctx, "LOCK TABLE "+qualified(o)+" IN EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = r.Loader(short, s.ProfileID(), o.Schema, o.Name, 0, 1)
	stop()
	if err == nil {
		t.Fatal("native page ignored another connection's exclusive table lock")
	}
	t.Log("native page rejected while original exclusive lock held", err)
	if err = lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	err = s.dataRead(ctx, o, func(ctx context.Context, q *sql.Conn, _ *contract.TableStructure) error {
		short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		defer stop()
		var count int64
		return q.QueryRowContext(short, `SELECT COUNT(*) FROM SYS.SYSOBJECTS A CROSS JOIN SYS.SYSOBJECTS B CROSS JOIN SYS.SYSOBJECTS C WHERE MOD(A.ID,17)+MOD(B.ID,17)+MOD(C.ID,17)=0`).Scan(&count)
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("active native data read did not acknowledge original cancellation", err)
	}
	page, err := r.Loader(ctx, s.ProfileID(), o.Schema, o.Name, 0, 1)
	if err != nil || page.Rows[0][3].Text != "concurrent" {
		t.Fatal("post-cancellation native read did not recover with new request", err, page)
	}
	if !reflect.DeepEqual(page.RowRefs[0].Columns, []string{"ID"}) || page.RowRefs[0].TypedValues[0].Text != strconv.FormatInt(9007199254740993, 10) {
		t.Fatal("original row identity changed")
	}
}

func TestNativeDamengDataReadOrdinaryGrantsAndExactCompositeKeys(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c, _ := nativeFixture(t)
	user := "NYADB_READ_USER_" + strings.ToUpper(nativeSuffix(t))
	secret := "Aa9!" + nativeSuffix(t)
	if _, err := admin.ExecContext(ctx, "CREATE USER "+quote(user)+" IDENTIFIED BY "+quote(secret)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP USER "+quote(user)+" CASCADE") })
	if _, err := admin.ExecContext(ctx, "GRANT SELECT ON "+qualified(o)+" TO "+quote(user)); err != nil {
		t.Fatal(err)
	}
	c.Username, c.Options.DefaultSchema, c.Options.MaxConnections = user, user, 1
	c = accountCertificate(t, c)
	ordinary := fixtureSession(t, c, secret)
	page, err := ordinary.DataReads().AdvancedLoader(ctx, ordinary.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1, UseKeyset: true})
	if err != nil || len(page.Rows) != 1 || !page.StableOrder || page.RowRefs[0] == nil {
		for _, query := range []string{
			`SELECT OWNER,CONSTRAINT_NAME,INDEX_OWNER,INDEX_NAME,STATUS,VALIDATED,DEFERRABLE FROM SYS.ALL_CONSTRAINTS WHERE OWNER=? AND TABLE_NAME=? AND CONSTRAINT_TYPE='P'`,
			`SELECT OWNER,CONSTRAINT_NAME,COLUMN_NAME,POSITION FROM SYS.ALL_CONS_COLUMNS WHERE TABLE_NAME=?`,
			`SELECT OWNER,INDEX_NAME,INDEX_TYPE,UNIQUENESS,STATUS,PARTITIONED,FUNCIDX_STATUS,ITYP_OWNER FROM SYS.ALL_INDEXES WHERE TABLE_OWNER=? AND TABLE_NAME=?`,
		} {
			args := []any{o.Schema, o.Name}
			if strings.Contains(query, "ALL_CONS_COLUMNS") {
				args = []any{o.Name}
			}
			rows, e := ordinary.db.QueryContext(ctx, query, args...)
			if e != nil {
				t.Log("native key proof diagnostic", e)
				continue
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				v, dest := make([]any, len(cols)), make([]any, len(cols))
				for i := range v {
					dest[i] = &v[i]
				}
				if e = rows.Scan(dest...); e != nil {
					break
				}
				t.Log("native key proof diagnostic", cols, v)
			}
			rows.Close()
		}
		t.Fatal("ordinary SELECT grant did not support bounded locked reads", err)
	}
	if row, e := ordinary.DataReads().RowReader(ctx, ordinary.ProfileID(), o.Schema, o.Name, *page.RowRefs[0]); e != nil || len(row.Values["B"].Bytes) != 5600 {
		t.Fatal("ordinary SELECT grant did not support complete SHARE-locked reads", e)
	}
	if _, err = admin.ExecContext(ctx, "CREATE TABLE "+quote(o.Schema)+`."UnGranted"(ID INT PRIMARY KEY,T VARCHAR(32))`); err != nil {
		t.Fatal(err)
	}
	if _, err = ordinary.DataReads().Loader(ctx, ordinary.ProfileID(), o.Schema, "UnGranted", 0, 1); err == nil {
		t.Fatal("ordinary read exposed ungranted table")
	}
	composite := s.Namespace(o.Schema).Object("table", "Composite")
	if _, err = admin.ExecContext(ctx, "CREATE TABLE "+qualified(composite)+"(A INT NOT NULL,D DECIMAL(38,20) NOT NULL,T VARCHAR(32),PRIMARY KEY(A,D))"); err != nil {
		t.Fatal(err)
	}
	for i, n := range []string{"123456789012345678.12345678901234567891", "123456789012345678.12345678901234567892", "123456789012345678.12345678901234567893"} {
		if _, err = admin.ExecContext(ctx, "INSERT INTO "+qualified(composite)+" VALUES(?,?,?)", int64(1), dpi.Decimal(n), strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	var after *contract.DataCursor
	for i := 0; i < 3; i++ {
		p, e := s.DataReads().AdvancedLoader(ctx, s.ProfileID(), o.Schema, composite.Name, contract.DataOptions{Limit: 1, UseKeyset: true, After: after})
		if e != nil || len(p.Rows) != 1 || !p.StableOrder || p.Rows[0][2].Text != strconv.Itoa(i) || !reflect.DeepEqual(p.RowRefs[0].Columns, []string{"A", "D"}) {
			t.Fatal("exact composite seek skipped/repeated a native decimal boundary", i, e, p)
		}
		chunk, e := s.DataReads().Chunk(ctx, s.ProfileID(), o.Schema, composite.Name, "T", *p.RowRefs[0], 0, 32)
		if e != nil || chunk.Text != strconv.Itoa(i) {
			t.Fatal("typed composite row reference lost decimal precision", e, chunk)
		}
		if row, e := s.DataReads().RowReader(ctx, s.ProfileID(), o.Schema, composite.Name, *p.RowRefs[0]); e != nil || row.Values["D"].Text != p.Rows[0][1].Text || row.Values["T"].Text != strconv.Itoa(i) {
			t.Fatal("complete composite snapshot lost exact native decimal identity", e)
		}
		after = p.NextCursor
	}
	identity := s.Namespace(o.Schema).Object("table", "IdentityKey")
	if _, err = admin.ExecContext(ctx, "CREATE TABLE "+qualified(identity)+"(ID BIGINT IDENTITY(1,1) PRIMARY KEY,T VARCHAR(32))"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ExecContext(ctx, "INSERT INTO "+qualified(identity)+"(T) VALUES('generated')"); err != nil {
		t.Fatal(err)
	}
	generated, err := s.DataReads().AdvancedLoader(ctx, s.ProfileID(), o.Schema, identity.Name, contract.DataOptions{Limit: 1, UseKeyset: true})
	if err != nil || !generated.StableOrder || generated.RowRefs[0] == nil || generated.ColumnMetadata[0].Extra != "IDENTITY" {
		t.Fatal("native identity primary key was not reliable for read navigation", err, generated)
	}
}
