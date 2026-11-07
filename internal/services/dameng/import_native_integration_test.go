package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func TestNativeDamengImportPhysicalReturningAndDDLCommitSemantics(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID BIGINT DEFAULT 9007199254740993 PRIMARY KEY,I BIGINT IDENTITY(9007199254740993,1),V VARCHAR(64) DEFAULT 'default'")
	c, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var tx dpi.ContextTransaction
	begin := func() {
		err = c.Raw(func(raw any) error {
			v, e := raw.(driver.ConnBeginTx).BeginTx(ctx, driver.TxOptions{})
			if e == nil {
				tx = v.(dpi.ContextTransaction)
			}
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	begin()
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()
	var rid sql.NullString
	var affected int64
	if _, err = c.ExecContext(ctx, "BEGIN INSERT INTO "+qualified(o)+"(V) VALUES(DEFAULT) RETURNING ROWID INTO ?; ? := SQL%ROWCOUNT; END;", sql.Out{Dest: &rid}, sql.Out{Dest: &affected}); err != nil {
		t.Fatal("native insert RETURNING ROWID", err)
	}
	if !rid.Valid || rid.String == "" || affected != 1 {
		t.Fatal("native physical insert receipt", rid, affected)
	}
	var id int64
	if err = c.QueryRowContext(ctx, "SELECT ID FROM "+qualified(o)+" WHERE ROWID=?", rid.String).Scan(&id); err != nil || id != 9007199254740993 {
		t.Fatal("actual identity through original ROWID", err, id)
	}
	old := rid.String
	if _, err = c.ExecContext(ctx, "BEGIN UPDATE "+qualified(o)+" SET ID=ID+10,V='updated' WHERE ROWID=? RETURNING ROWID INTO ?; ? := SQL%ROWCOUNT; END;", old, sql.Out{Dest: &rid}, sql.Out{Dest: &affected}); err != nil {
		t.Fatal("native update physical receipt", err)
	}
	if affected != 1 || !rid.Valid {
		t.Fatal("native update receipt invalid")
	}
	if err = c.QueryRowContext(ctx, "SELECT ID FROM "+qualified(o)+" WHERE ROWID=?", rid.String).Scan(&id); err != nil || id != 9007199254741003 {
		t.Fatal("updated original physical row", err, id)
	}
	t.Log("native changed primary key ROWID stable:", old == rid.String)
	if _, err = c.ExecContext(ctx, "BEGIN DELETE FROM "+qualified(o)+" WHERE ROWID=? RETURNING ROWID INTO ?; ? := SQL%ROWCOUNT; END;", rid.String, sql.Out{Dest: &rid}, sql.Out{Dest: &affected}); err != nil {
		t.Fatal("native delete physical receipt", err)
	}
	if affected != 1 || !rid.Valid {
		t.Fatal("native delete original receipt")
	}
	if err = tx.RollbackContext(ctx); err != nil {
		t.Fatal(err)
	}
	tx = nil
	begin()
	if _, err = c.ExecContext(ctx, "INSERT INTO "+qualified(o)+"(V) VALUES('before DDL')"); err != nil {
		t.Fatal(err)
	}
	newObject := s.Namespace("SYSDBA").Object("table", "NYADB_IMPORT_DDL_"+nativeSuffix(t))
	t.Cleanup(func() { s.db.ExecContext(context.Background(), "DROP TABLE "+qualified(newObject)) })
	if _, err = c.ExecContext(ctx, "CREATE TABLE "+qualified(newObject)+"(ID INT)"); err != nil {
		t.Fatal(err)
	}
	if err = tx.RollbackContext(ctx); err != nil {
		t.Fatal(err)
	}
	tx = nil
	var count int64
	if err = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 1 {
		t.Fatal("DDL must commit the preceding original DML", err, count)
	}
	if err = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(newObject)).Scan(&count); err != nil || count != 0 {
		t.Fatal("new native table missing or changed after rollback", err, count)
	}
}
