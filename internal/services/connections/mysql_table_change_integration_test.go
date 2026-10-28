package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// Opt-in: provision a disposable MySQL 8.x server bound to 127.0.0.1,
// schema nyadb_table_it, and these accounts (passwords may be supplied through
// NYADB_MYSQL_TABLE_{WRITER,READER,COPY}_PASSWORD):
//   - nyadb_table_writer: ALL on nyadb_table_it.*
//   - nyadb_table_reader: SELECT on nyadb_table_it.*
//   - nyadb_table_copy: SELECT, CREATE on nyadb_table_it.* (no INSERT)
//
// Enable NYADB_MYSQL_TABLE_IT=1 and set NYADB_MYSQL_TABLE_PORT. This test creates
// and removes only its own uniquely named tables; never use a production server.
func TestMySQLTableChangesIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_TABLE_IT") != "1" {
		t.Skip("requires explicit disposable MySQL loopback fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_TABLE_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("missing valid disposable loopback port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	t.Cleanup(func() { _ = m.Close() })
	for i, role := range []string{"writer", "reader", "copy"} {
		if err := m.Connect(ctx, ActiveSettings{
			ProfileID: int64(i + 1), Host: "127.0.0.1", Port: uint16(port),
			Username: "nyadb_table_" + role, MySQLAdvanced: DefaultMySQLAdvanced(),
		}, os.Getenv("NYADB_MYSQL_TABLE_"+strings.ToUpper(role)+"_PASSWORD")); err != nil {
			t.Fatalf("connect %s: %v", role, err)
		}
	}
	db := m.sessions[1].db
	const schema = "nyadb_table_it"
	prefix := fmt.Sprintf("nya_it_%d_", time.Now().UnixNano())
	qualified := func(name string) string { return "`" + schema + "`.`" + name + "`" }
	exec := func(t *testing.T, statement string, args ...any) sql.Result {
		t.Helper()
		result, err := db.ExecContext(ctx, statement, args...)
		if err != nil {
			t.Fatalf("fixture SQL: %v", err)
		}
		return result
	}
	name := func(t *testing.T, suffix string) string {
		t.Helper()
		n := prefix + suffix
		t.Cleanup(func() {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if _, err := db.ExecContext(cleanupCtx, "DROP TABLE IF EXISTS "+qualified(n)); err != nil {
				t.Errorf("remove test table %s: %v", n, err)
			}
		})
		return n
	}
	seed := func(t *testing.T, suffix string) string {
		t.Helper()
		n := name(t, suffix)
		exec(t, "CREATE TABLE "+qualified(n)+" (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, note VARCHAR(80) NOT NULL, KEY note_idx(note)) ENGINE=InnoDB AUTO_INCREMENT=41")
		exec(t, "INSERT INTO "+qualified(n)+" (note) VALUES ('alpha'),('beta')")
		return n
	}
	count := func(t *testing.T, n string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(n)).Scan(&got); err != nil || got != want {
			t.Fatalf("table %s row count = %d, want %d, err=%v", n, got, want, err)
		}
	}
	exists := func(t *testing.T, n string, want bool) {
		t.Helper()
		var got int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", schema, n).Scan(&got); err != nil || (got == 1) != want {
			t.Fatalf("table %s existence = %d, want %v, err=%v", n, got, want, err)
		}
	}
	nextID := func(t *testing.T, n string, want int64) {
		t.Helper()
		result := exec(t, "INSERT INTO "+qualified(n)+" (note) VALUES ('after')")
		got, err := result.LastInsertId()
		if err != nil || got != want {
			t.Fatalf("table %s next ID = %d, want %d, err=%v", n, got, want, err)
		}
	}
	change := func(t *testing.T, action MySQLTableAction, source, target string) {
		t.Helper()
		if err := m.ChangeTable(ctx, 1, MySQLTableChange{Action: action, Database: schema, Table: source, NewName: target}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	t.Run("create", func(t *testing.T) {
		n := name(t, "create")
		change(t, MySQLTableCreate, n, "")
		count(t, n, 0)
		var kind, extra, key string
		if err := db.QueryRowContext(ctx, "SELECT DATA_TYPE,EXTRA,COLUMN_KEY FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND COLUMN_NAME='id'", schema, n).Scan(&kind, &extra, &key); err != nil {
			t.Fatal(err)
		}
		if kind != "bigint" || extra != "auto_increment" || key != "PRI" {
			t.Fatalf("starter id metadata: %q %q %q", kind, extra, key)
		}
		result := exec(t, "INSERT INTO "+qualified(n)+" () VALUES ()")
		if id, err := result.LastInsertId(); err != nil || id != 1 {
			t.Fatalf("starter first ID = %d, err=%v", id, err)
		}
	})
	t.Run("rename", func(t *testing.T) {
		source, target := seed(t, "rename_source"), name(t, "renamed")
		change(t, MySQLTableRename, source, target)
		exists(t, source, false)
		count(t, target, 2)
		nextID(t, target, 43)
	})
	t.Run("delete_rows_preserves_sequence", func(t *testing.T) {
		n := seed(t, "delete")
		change(t, MySQLTableDeleteRows, n, "")
		count(t, n, 0)
		nextID(t, n, 43)
	})
	t.Run("truncate_resets_sequence", func(t *testing.T) {
		n := seed(t, "truncate")
		change(t, MySQLTableTruncate, n, "")
		count(t, n, 0)
		nextID(t, n, 1)
	})
	t.Run("drop_removes_structure", func(t *testing.T) {
		n := seed(t, "drop")
		change(t, MySQLTableDrop, n, "")
		exists(t, n, false)
	})
	for _, action := range []MySQLTableAction{MySQLTableCopyStructure, MySQLTableCopyData} {
		t.Run(string(action), func(t *testing.T) {
			source, target := seed(t, string(action)+"_source"), name(t, string(action)+"_target")
			change(t, action, source, target)
			wantRows := 0
			if action == MySQLTableCopyData {
				wantRows = 2
				var got string
				if err := db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(CONCAT(id,':',note) ORDER BY id) FROM "+qualified(target)).Scan(&got); err != nil || got != "41:alpha,42:beta" {
					t.Fatalf("copied values = %q, err=%v", got, err)
				}
			}
			count(t, target, wantRows)
			count(t, source, 2)
			var columns, indexes int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", schema, target).Scan(&columns); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", schema, target).Scan(&indexes); err != nil {
				t.Fatal(err)
			}
			if columns != 2 || indexes != 2 {
				t.Fatalf("copied structure: columns=%d indexes=%d", columns, indexes)
			}
		})
	}
	t.Run("read_only_denied", func(t *testing.T) {
		source := seed(t, "readonly_source")
		for _, action := range []MySQLTableAction{MySQLTableCreate, MySQLTableRename, MySQLTableDrop, MySQLTableDeleteRows, MySQLTableTruncate, MySQLTableCopyStructure, MySQLTableCopyData} {
			t.Run(string(action), func(t *testing.T) {
				target := name(t, "readonly_"+string(action))
				request := MySQLTableChange{Action: action, Database: schema, Table: source, NewName: target}
				if action == MySQLTableCreate {
					request.Table = target
				}
				err := m.ChangeTable(ctx, 2, request)
				var serverErr *mysql.MySQLError
				if !errors.As(err, &serverErr) || serverErr.Number != 1142 {
					t.Fatalf("expected command denied (1142), got %v", err)
				}
				count(t, source, 2)
				exists(t, target, false)
			})
		}
	})
	t.Run("foreign_key_denied", func(t *testing.T) {
		parent := seed(t, "fk_parent")
		child := name(t, "fk_child")
		exec(t, "CREATE TABLE "+qualified(child)+" (id BIGINT PRIMARY KEY, parent_id BIGINT, FOREIGN KEY(parent_id) REFERENCES "+qualified(parent)+"(id)) ENGINE=InnoDB")
		exec(t, "INSERT INTO "+qualified(child)+" VALUES (1,41)")
		for _, action := range []MySQLTableAction{MySQLTableDeleteRows, MySQLTableTruncate, MySQLTableDrop} {
			err := m.ChangeTable(ctx, 1, MySQLTableChange{Action: action, Database: schema, Table: parent})
			var serverErr *mysql.MySQLError
			if !errors.As(err, &serverErr) || (serverErr.Number != 1451 && serverErr.Number != 1701 && serverErr.Number != 3730) {
				t.Fatalf("%s expected FK restriction, got %v", action, err)
			}
			count(t, parent, 2)
			count(t, child, 1)
		}
	})
	t.Run("copy_generated_columns", func(t *testing.T) {
		source, target := name(t, "generated_source"), name(t, "generated_target")
		exec(t, "CREATE TABLE "+qualified(source)+" (id INT PRIMARY KEY, amount INT, doubled INT GENERATED ALWAYS AS (amount * 2) STORED, tripled INT GENERATED ALWAYS AS (amount * 3) VIRTUAL) ENGINE=InnoDB")
		exec(t, "INSERT INTO "+qualified(source)+" (id,amount) VALUES (1,7),(2,NULL)")
		change(t, MySQLTableCopyData, source, target)
		count(t, target, 2)
		var doubled, tripled int
		if err := db.QueryRowContext(ctx, "SELECT doubled,tripled FROM "+qualified(target)+" WHERE id=1").Scan(&doubled, &tripled); err != nil || doubled != 14 || tripled != 21 {
			t.Fatalf("generated values = %d,%d, err=%v", doubled, tripled, err)
		}
		var nulls int
		if err := db.QueryRowContext(ctx, "SELECT doubled IS NULL AND tripled IS NULL FROM "+qualified(target)+" WHERE id=2").Scan(&nulls); err != nil || nulls != 1 {
			t.Fatalf("generated NULL values not preserved: %d, err=%v", nulls, err)
		}
	})
	t.Run("copy_invisible_and_default_generated_columns", func(t *testing.T) {
		source, target := name(t, "invisible_source"), name(t, "invisible_target")
		exec(t, "CREATE TABLE "+qualified(source)+" (id INT PRIMARY KEY, `odd``name` INT INVISIBLE DEFAULT 17, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP) ENGINE=InnoDB")
		exec(t, "INSERT INTO "+qualified(source)+" (id,`odd``name`,created_at) VALUES (1,99,'2020-01-02 03:04:05')")
		change(t, MySQLTableCopyData, source, target)
		var hidden int
		var created string
		if err := db.QueryRowContext(ctx, "SELECT `odd``name`,CAST(created_at AS CHAR) FROM "+qualified(target)+" WHERE id=1").Scan(&hidden, &created); err != nil || hidden != 99 || created != "2020-01-02 03:04:05" {
			t.Fatalf("invisible/default-generated values = %d,%q, err=%v", hidden, created, err)
		}
	})
	t.Run("partial_copy_preserves_destination", func(t *testing.T) {
		source, target := seed(t, "partial_source"), name(t, "partial_target")
		err := m.ChangeTable(ctx, 3, MySQLTableChange{Action: MySQLTableCopyData, Database: schema, Table: source, NewName: target})
		var serverErr *mysql.MySQLError
		if !errors.As(err, &serverErr) || serverErr.Number != 1142 || !strings.Contains(err.Error(), "new table remains") {
			t.Fatalf("expected explicit partial-copy failure, got %v", err)
		}
		count(t, source, 2)
		count(t, target, 0)
	})
}
