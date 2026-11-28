package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func originalSQLReceipt(t *testing.T, r contract.SQLResult) nativeSQLReceipt {
	t.Helper()
	var original nativeSQLReceipt
	if e := json.Unmarshal([]byte(r.NativeJSON), &original); e != nil {
		t.Fatal("missing original receipt", e)
	}
	if len(original.Statements) != len(r.Steps) {
		t.Fatal("receipt lost original steps")
	}
	for i, step := range original.Statements {
		var item nativeSQLStatement
		if e := json.Unmarshal([]byte(r.Steps[i].NativeJSON), &item); e != nil {
			t.Fatal(e)
		}
		a, _ := json.Marshal(item)
		b, _ := json.Marshal(step)
		if string(a) != string(b) || step.State != r.Steps[i].State || step.SQL != r.Steps[i].SQL {
			t.Fatal("per-statement receipt diverged")
		}
	}
	return original
}
func TestNativeSQLOriginalNegativeCountsIDsAndFileReceipts(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7301)
	table := createNativeTable(t, q)
	percolate := fmt.Sprintf("nyadb_sql_pq73_%d", time.Now().UnixNano())
	runNative(t, q, "CREATE TABLE "+percolate+" (title text) type='pq'")
	defer runNative(t, q, "DROP TABLE "+percolate)
	runNative(t, q, "INSERT INTO "+percolate+" (id,query,tags) VALUES (1,'original','before')")
	source := "UPDATE " + percolate + " SET tags='ignored' WHERE id=1; INSERT INTO " + table + " (id,qty) VALUES (1,7); SELECT id,tags FROM " + percolate + " ORDER BY id ASC"
	r, e := q.(contract.QueryFileLease).ExecuteFile(context.Background(), "Manticore", strings.NewReader(source))
	if e != nil {
		t.Fatal(e)
	}
	original := originalSQLReceipt(t, r)
	if r.AffectedRows != -1 || len(r.Sets) != 3 || r.Sets[0].AffectedRows != -1 || r.Sets[1].AffectedRows != 1 || r.Sets[2].Rows[0][1].Text != "before" {
		t.Fatal("negative original count converted to successful row change", r)
	}
	if original.Object != s.Namespace("Manticore") || *original.Statements[0].AffectedRows != -1 || !original.Statements[0].Acknowledged || original.Statements[2].AffectedRows != nil {
		t.Fatal(original)
	}
	if e := q.(contract.QueryLeaseStatus).CheckQueryLease(); e != nil {
		t.Fatal("unreported count retired original connection", e)
	}
	objectEvidence(t, "native-sql-negative-file.json", r)
	r = runNative(t, q, "REPLACE INTO "+percolate+" (id,query,tags) VALUES (1,'replacement','after')")
	if r.AffectedRows != 1 || *originalSQLReceipt(t, r).Statements[0].AffectedRows != 1 {
		t.Fatal(r)
	}
	actual := runNative(t, q, "SELECT query,tags FROM "+percolate+" WHERE id=1")
	if actual.Rows[0][0] != "replacement" || actual.Rows[0][1] != "after" {
		t.Fatal("explicit replacement did not change row", actual)
	}
	r = runNative(t, q, "INSERT INTO "+table+" (id,qty) VALUES (18446744073709551615,9)")
	if *originalSQLReceipt(t, r).Statements[0].LastInsertID != -1 {
		t.Fatal("original unsigned ID driver representation lost", r)
	}
	objectEvidence(t, "native-sql-unsigned-id.json", r)
	source = "INSERT INTO " + table + " (id,qty) VALUES (2,20); INSERT INTO " + table + " (id,qty) VALUES (2,99); INSERT INTO " + table + " (id,qty) VALUES (3,30)"
	r, e = q.Execute(context.Background(), "Manticore", source)
	if e == nil || errors.Is(e, contract.ErrSQLSessionLost) || len(r.Steps) != 3 {
		t.Fatal(r, e)
	}
	original = originalSQLReceipt(t, r)
	for i, state := range []string{"completed", "failed", "not_executed"} {
		if original.Statements[i].State != state {
			t.Fatal(original)
		}
	}
	if original.Statements[1].ErrorCode == 0 || original.Statements[1].SQLState == "" || !original.Statements[1].Acknowledged || original.Statements[2].Dispatched || r.AffectedRows != 1 {
		t.Fatal(original)
	}
	check := runNative(t, q, "SELECT id,qty FROM "+table+" WHERE id IN (2,3) ORDER BY id ASC")
	if len(check.Rows) != 1 || check.Rows[0][1] != "20" {
		t.Fatal("prefix/suffix changed", check)
	}
	objectEvidence(t, "native-sql-failed-prefix.json", r)
	// Preserve exactly what the server sends, including its own ellipsis.
	name := "bad_" + strings.Repeat("x", 700)
	source = "SELECT " + name + " FROM " + table
	c, e := s.connect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_, nativeError := read(context.Background(), c, source, 0, nil, nil)
	var serverError *mysqldriver.MySQLError
	if !errors.As(nativeError, &serverError) {
		t.Fatal("expected original server error", nativeError)
	}
	r, e = q.Execute(context.Background(), "Manticore", source)
	if e == nil {
		t.Fatal("expected actual server rejection")
	}
	original = originalSQLReceipt(t, r)
	step := original.Statements[0]
	if step.ErrorCode != serverError.Number || step.SQLState != string(serverError.SQLState[:]) || step.Error != serverError.Message {
		t.Fatal("original server error changed", original, serverError)
	}
	objectEvidence(t, "native-sql-full-server-error.json", r)
}
func TestNativeSQLOriginalUnknownPrefixAndUnsentSuffix(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7302)
	table := createNativeTable(t, q)
	for index, mode := range []string{"drop", "cancel", "release"} {
		t.Run(mode, func(t *testing.T) {
			gate := newPacketGate(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
			c := cfg
			host, port, _ := net.SplitHostPort(gate.listener.Addr().String())
			n, _ := strconv.Atoi(port)
			c.Host, c.Port = host, uint16(n)
			s := openNative(t, c)
			lease := nativeLease(t, s, 1)
			first, second, third := index*10+1, index*10+2, index*10+3
			statement := func(id int) string { return fmt.Sprintf("INSERT INTO %s (id,qty) VALUES (%d,7)", table, id) }
			source := statement(first) + "; " + statement(second) + "; " + statement(third)
			observed := gate.arm(statement(second), mode)
			type response struct {
				r contract.SQLResult
				e error
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan response, 1)
			go func() {
				r, e := lease.(contract.QueryFileLease).ExecuteFile(ctx, "Manticore", strings.NewReader(source))
				done <- response{r, e}
			}()
			select {
			case ack := <-observed:
				if len(ack) < 6 || ack[4] != 0 {
					t.Fatalf("missing original OK %x", ack)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no original response")
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "release" {
				lease.Release()
			}
			var out response
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("executor did not settle")
			}
			if !errors.Is(out.e, contract.ErrSQLSessionLost) {
				t.Fatal(out.e)
			}
			original := originalSQLReceipt(t, out.r)
			for i, state := range []string{"completed", "uncertain", "not_executed"} {
				if original.Statements[i].State != state {
					t.Fatal(original)
				}
			}
			if !original.Statements[1].Dispatched || original.Statements[1].Acknowledged || original.Statements[1].AffectedRows != nil || original.Statements[2].Dispatched || out.r.AffectedRows != 1 {
				t.Fatal(original)
			}
			saved := out.r.NativeJSON
			if _, e := lease.Execute(context.Background(), "Manticore", source); e == nil || gate.calls.Load() != 1 {
				t.Fatal("lost lease replayed")
			}
			replacement, e := lease.Replace()
			if e != nil {
				t.Fatal(e)
			}
			defer replacement.Release()
			if e = replacement.Preconnect(context.Background()); e != nil {
				t.Fatal(e)
			}
			check := runNative(t, q, fmt.Sprintf("SELECT id FROM %s WHERE id IN (%d,%d,%d) ORDER BY id ASC", table, first, second, third))
			if len(check.Rows) != 2 || check.Rows[0][0] != strconv.Itoa(first) || check.Rows[1][0] != strconv.Itoa(second) || out.r.NativeJSON != saved || gate.calls.Load() != 1 {
				t.Fatal("readback repaired original unknown or recovery replayed", check)
			}
			objectEvidence(t, "native-sql-unknown-"+mode+".json", out.r)
		})
	}
}
