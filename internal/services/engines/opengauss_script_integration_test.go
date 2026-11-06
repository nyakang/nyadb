package engines

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestOpenGaussNativeSQLFilesAndCancellationFixture(t *testing.T) {
	s, _ := openGaussFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	l, err := s.ReserveQuery(700)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	file, ok := l.(contract.QueryFileLease)
	if !ok {
		t.Fatal("openGauss SQL file feature is absent")
	}
	run := func(text string) contract.SQLResult {
		t.Helper()
		out, err := l.Execute(ctx, "", text)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	schema := fmt.Sprintf("nyadb_gauss_script_%d", time.Now().UnixNano())
	run("CREATE SCHEMA " + quote(schema))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := s.db.ExecContext(cleanup, "DROP SCHEMA "+quote(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	before := run("SELECT pg_backend_pid()").Rows[0][0]
	script := `CREATE TABLE marks(id INTEGER PRIMARY KEY,note TEXT);
CREATE OR REPLACE PROCEDURE write_marks(note TEXT) AS
DECLARE n INTEGER := 0;
BEGIN
  IF note IS NOT NULL THEN
    FOR n IN 1..2 LOOP
      INSERT INTO marks VALUES(n,note);
    END LOOP;
  END IF;
END;
/
CREATE FUNCTION next_value(n BIGINT) RETURN BIGINT AS
BEGIN RETURN n+1; END;
/
CALL write_marks('中文; END;');
SELECT note,next_value(id) FROM marks ORDER BY id;`
	out, err := file.ExecuteFile(ctx, schema, strings.NewReader("\uFEFF"+script))
	if err != nil || len(out.Sets) != 5 || len(out.Steps) != 5 {
		t.Fatal("native procedure/function SQL file", err, out.Steps)
	}
	last := out.Sets[4]
	if len(last.Rows) != 2 || last.Rows[0][0].Text != "中文; END;" || last.Rows[1][1].Text != "3" {
		t.Fatal("native procedure/function results", last)
	}
	if after := run("SELECT pg_backend_pid()").Rows[0][0]; after != before {
		t.Fatal("SQL file replaced its original physical session")
	}
	// A lexical error later in a file must prevent its earlier INSERT.
	if _, err = file.ExecuteFile(ctx, schema, strings.NewReader("INSERT INTO marks VALUES(7,'must not run'); CREATE PROCEDURE broken() AS BEGIN NULL;")); err == nil {
		t.Fatal("incomplete native file accepted")
	}
	if run("SELECT count(*) FROM marks WHERE id=7").Rows[0][0] != "0" {
		t.Fatal("file validation happened after an earlier write")
	}
	// The same physical session can execute one native body and stream values.
	if _, err = l.Execute(ctx, schema, "CREATE OR REPLACE PROCEDURE native_caret() AS BEGIN NULL; END;"); err != nil {
		t.Fatal("native caret request needs a client slash", err)
	}
	streamed := 0
	if _, err = l.ExecuteStream(ctx, schema, "SELECT id FROM marks ORDER BY id", func(_ int, _ []contract.Column, values []contract.Cell) error {
		if values == nil {
			return nil
		}
		streamed++
		if values[0].Kind != contract.Integer {
			t.Fatal("stream changed typed value")
		}
		return nil
	}); err != nil || streamed != 2 {
		t.Fatal("native streaming", err, streamed)
	}
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = file.ExecuteFile(short, schema, strings.NewReader("BEGIN; INSERT INTO marks VALUES(9,'cancelled'); SELECT pg_sleep(5); COMMIT; INSERT INTO marks VALUES(10,'must not replay');"))
	stop()
	if !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("file cancellation did not wait for session loss", err)
	}
	if _, err = file.ExecuteFile(ctx, schema, strings.NewReader("INSERT INTO marks VALUES(10,'must not replay');")); !errors.Is(err, contract.ErrSQLSessionLost) && !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("cancelled file silently acquired another session", err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+quote(schema)+".marks WHERE id IN (9,10)").Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled file committed/replayed writes", err, count)
	}
}
