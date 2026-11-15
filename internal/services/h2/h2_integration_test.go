package h2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

func managedH2Fixture(t *testing.T) (Provider, connections.H2Options, string, string) {
	t.Helper()
	if os.Getenv("NYADB_H2_PROVIDER_IT") != "1" {
		t.Skip("requires owned managed H2 fixture")
	}
	var f struct{ Archive, SHA256, Directory, PasswordFile string }
	raw, err := os.ReadFile(os.Getenv("NYADB_MANAGED_H2_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("H2 fixture unavailable")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal("H2 fixture credential unavailable")
	}
	store, err := driverstore.New(filepath.Join(f.Directory, "provider-store"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	m, err := store.Install(ctx, in, f.SHA256)
	in.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Remove(context.Background(), m.Key, m.Version); err != nil {
			t.Error(err)
		}
	})
	provider := Provider{Store: store, Started: func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 20 {
			return fmt.Errorf("original process identity unavailable")
		}
		cmd, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return err
		}
		args := strings.Split(string(cmd), "\x00")
		agent := ""
		for i, arg := range args {
			if arg == "-cp" && i+1 < len(args) {
				agent = strings.Split(args[i+1], string(os.PathListSeparator))[0]
			}
		}
		if agent == "" {
			return fmt.Errorf("original classpath unavailable")
		}
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "java": args[0], "agent": agent, "jdbc_arguments": args[1 : len(args)-1]})
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		_, err = journal.Write(append(record, '\n'))
		return err
	}}
	a := connections.DefaultH2Options()
	a.DriverVersion, a.DriverSHA256 = m.Version, f.SHA256
	a.ConnectTimeoutSeconds = 120
	return provider, a, strings.TrimSpace(string(secret)), f.Directory
}

func TestH2ManagedProviderQualifiedMetadataAndQueryLeases(t *testing.T) {
	provider, options, password, _ := managedH2Fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, v := range []struct{ profile, version string }{{"h2-v1", "1.4.200"}, {"h2-v2", "2.1.214"}, {"h2-v3", "2.4.240"}, {"h2-legacy", "2.1.214"}} {
		t.Run(v.version, func(t *testing.T) {
			a := options
			a.Mode = "memory"
			a.AutoServer = false
			a.RequireExisting = false
			a.DriverProfile = v.profile
			p := connections.Profile{ID: 17, Kind: connections.H2, Name: "owned H2", Database: "owned_provider", Username: "sa", H2Options: a}
			raw, err := provider.Open(ctx, p, password)
			if err != nil {
				t.Fatal(err)
			}
			s := raw.(*Session)
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			lease, err := s.ReserveQuery(1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(lease.Release)
			q, err := lease.Execute(ctx, "PUBLIC", `CREATE TABLE "quoted;records"("id" BIGINT PRIMARY KEY,"amount" DECIMAL(38,20),"bytes" VARBINARY,"note" VARCHAR); INSERT INTO "quoted;records" VALUES(9007199254740993,123456789012345678.12345678901234567890,X'00FF',NULL)`)
			if err != nil || len(q.Steps) != 2 || q.AffectedRows != 1 {
				t.Fatalf("original native script: %v", err)
			}
			names, err := s.Namespaces(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var ns contract.NamespaceRef
			for _, n := range names {
				if n.Ref.Schema == "PUBLIC" {
					ns = n.Ref
				}
			}
			if ns.ProfileID != p.ID || ns.Catalog != s.Catalog() || ns.Catalog == "" {
				t.Fatal("native catalog/schema identity lost")
			}
			objects, err := s.Browse(ctx, ns)
			if err != nil {
				t.Fatal(err)
			}
			var object contract.ObjectRef
			for _, o := range objects {
				if o.Name == "quoted;records" {
					object = o.Ref
				}
			}
			if object != ns.Object("table", "quoted;records") {
				t.Fatal("native table identity changed")
			}
			structure, err := s.TableMetadata(ctx, object)
			if err != nil {
				t.Fatal(err)
			}
			if len(structure.Columns) != 4 || len(structure.Indexes) != 1 || structure.Definition == "" || structure.Columns[1].NumericPrecision == nil || *structure.Columns[1].NumericPrecision != 38 || structure.Columns[1].NumericScale == nil || *structure.Columns[1].NumericScale != 20 {
				t.Fatal("native metadata/precision/DDL lost")
			}
			if structure.Columns[0].Key != "PRI" || structure.Indexes[0].Columns[0].Name != "id" {
				t.Fatal("native primary index order lost")
			}
			testH2DataFlow(t, ctx, s, lease, object)
			testH2NativeTypesAndReadOnlyKeys(t, ctx, s, lease)
			testH2CanceledSave(t, ctx, s, lease, object)
			q, err = lease.Execute(ctx, "PUBLIC", `SELECT * FROM "quoted;records"`)
			if err != nil || q.Sets[0].Rows[0][0].Text != "9007199254740993" || q.Sets[0].Rows[0][1].Text != "123456789012345678.12345678901234567890" || q.Sets[0].Rows[0][2].Display() != "0x00ff" || q.Sets[0].Rows[0][3].Kind != contract.Null {
				t.Fatal("original typed values changed", err)
			}
			bad := object
			bad.Catalog = "different-original-catalog"
			if _, err = s.TableMetadata(ctx, bad); err == nil {
				t.Fatal("unqualified metadata request admitted")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = contract.ComposeData(s).Loader(ctx, p.ID, "PUBLIC", object.Name, 0, 1); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("disconnected data reader reopened implicitly", err)
			}
			if _, err = lease.Execute(ctx, "PUBLIC", "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("disconnected SQL lease reopened implicitly", err)
			}
		})
	}
}

func testH2CanceledSave(t *testing.T, ctx context.Context, s *Session, holder contract.QueryLease, object contract.ObjectRef) {
	t.Helper()
	r := contract.ComposeData(s)
	page, err := r.Loader(ctx, s.ProfileID(), "PUBLIC", object.Name, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := s.ReserveQuery(99)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Release()
	if _, err = holder.Execute(ctx, "PUBLIC", `BEGIN; UPDATE "quoted;records" SET "note"="note" WHERE "id"=9007199254740993`); err != nil {
		t.Fatal(err)
	}
	defer holder.Execute(ctx, "PUBLIC", "ROLLBACK")
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type completion struct {
		result *contract.DataEditResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, err := r.Saver(cancelCtx, s.ProfileID(), "PUBLIC", object.Name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "canceled original write"}}}})
		done <- completion{result, err}
	}()
	deadline := time.Now().Add(12 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		q, err := observer.Execute(ctx, "PUBLIC", "SELECT COUNT(*) FROM INFORMATION_SCHEMA.SESSIONS WHERE BLOCKER_ID IS NOT NULL")
		if err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		if len(q.Sets) == 1 && len(q.Sets[0].Rows) == 1 && q.Sets[0].Rows[0][0].Text != "0" {
			blocked = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	// H2 checks cancellation when its native row-lock wait wakes. Release
	// only this fixture's lock so the original executor can acknowledge it;
	// a lock timeout would exercise quarantine rather than cancellation.
	if _, err = holder.Execute(ctx, "PUBLIC", "ROLLBACK"); err != nil {
		<-done
		t.Fatal(err)
	}
	c := <-done
	if !blocked || !errors.Is(c.err, context.Canceled) || c.result == nil || c.result.CommitConfirmed {
		t.Fatal("cancellation did not observe the original blocked executor", c.err)
	}
	var receipt transactionReceipt
	if json.Unmarshal([]byte(c.result.NativeJSON), &receipt) != nil || len(receipt.Statements) != 1 || !receipt.Statements[0].Dispatched || !receipt.Statements[0].ExecutorAcknowledged && !receipt.Statements[0].ProcessExitConfirmed {
		t.Fatal("save cancellation returned before original executor settled")
	}
	if _, err = observer.Execute(ctx, "PUBLIC", "SELECT 1"); err != nil {
		t.Fatal("canceling data save revoked an independent SQL document", err)
	}
	current, err := r.Loader(ctx, s.ProfileID(), "PUBLIC", object.Name, 0, 1)
	if err != nil || !current.Rows[0][3].Null {
		t.Fatal("post-cancel read did not retain the original row", err)
	}
	if c.result.CommitUncertain {
		verified, err := r.Verify(ctx, s.ProfileID(), "PUBLIC", object.Name, c.result)
		if err != nil || verified.Status != "indeterminate" {
			t.Fatal("later read granted replay authority", err)
		}
	}
}

func testH2DataFlow(t *testing.T, ctx context.Context, s *Session, lease contract.QueryLease, object contract.ObjectRef) {
	t.Helper()
	long := strings.Repeat("a", 1023) + strings.Repeat("😀你好", 600)
	_, err := lease.Execute(ctx, "PUBLIC", `INSERT INTO "quoted;records" VALUES(9007199254740994,1.25,X'0000FFFF',`+textLiteral(long)+`)`)
	if err != nil {
		t.Fatal(err)
	}
	r := contract.ComposeData(s)
	page, err := r.AdvancedLoader(ctx, s.ProfileID(), "PUBLIC", object.Name, contract.DataOptions{Object: object, Limit: 1})
	if err != nil || len(page.Rows) != 1 || !page.HasMore || !page.StableOrder || page.RowSnapshots[0] == nil || !*page.Editable || page.Rows[0][0].Value.Text != "9007199254740993" || page.Rows[0][1].Value.Text != "123456789012345678.12345678901234567890" || page.Rows[0][2].Value.Display() != "0x00ff" || !page.Rows[0][3].Null {
		t.Fatal("typed page, stable key, NULL or original fingerprint lost", err)
	}
	second, err := r.Loader(ctx, s.ProfileID(), "PUBLIC", object.Name, 1, 1)
	if err != nil || len(second.Rows) != 1 || second.HasMore || !second.Rows[0][3].Truncated || second.Rows[0][3].Text != strings.Repeat("a", 1023) {
		t.Fatal("bounded UTF-8 preview split a code point", err)
	}
	ref := *second.RowRefs[0]
	chunk, err := r.Chunk(ctx, s.ProfileID(), "PUBLIC", object.Name, "note", ref, 0, 1024)
	if err != nil || chunk.Text != strings.Repeat("a", 1023) || chunk.NextOffset != 1023 || chunk.TotalUnits != int64(len(long)) || !chunk.HasMore {
		t.Fatal("original UTF-8 field units changed", err)
	}
	next, err := r.Chunk(ctx, s.ProfileID(), "PUBLIC", object.Name, "note", ref, chunk.NextOffset, 1024)
	if err != nil || !strings.HasPrefix(next.Text, "😀你好") || strings.ContainsRune(next.Text, '\ufffd') || next.NextOffset != chunk.NextOffset+int64(len(next.Text)) {
		t.Fatal("native field continuation corrupted UTF-8", err)
	}
	binary, err := r.Chunk(ctx, s.ProfileID(), "PUBLIC", object.Name, "bytes", ref, 1, 2)
	if err != nil || !binary.Binary || binary.Text != "0x00ff" || binary.TotalUnits != 4 || binary.NextOffset != 3 || !binary.HasMore {
		t.Fatal("native binary field units changed", err)
	}
	full, err := r.RowReader(ctx, s.ProfileID(), "PUBLIC", object.Name, ref)
	if err != nil || full.Values["note"].Text != long || full.Snapshot.Mode != "values" || full.Values["bytes"].Kind != contract.Binary {
		t.Fatal("complete row/snapshot changed", err)
	}
	filtered, err := r.AdvancedLoader(ctx, s.ProfileID(), "PUBLIC", object.Name, contract.DataOptions{Limit: 5, FilterColumn: "note", FilterMode: "contains", FilterValue: "😀你好"})
	if err != nil || len(filtered.Rows) != 1 || strings.Contains(filtered.QuerySQL, "😀你好") {
		t.Fatal("filter or value-free SQL presentation lost", err)
	}
	update := contract.DataEdit{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"note": {Text: "saved ' ; -- 😀"}, "bytes": {Binary: true, Bytes: []byte{0, 255, 1}}, "amount": {Text: "2.12345678901234567890"}}}
	saved, err := r.Saver(ctx, s.ProfileID(), "PUBLIC", object.Name, []contract.DataEdit{update})
	if err != nil || !saved.CommitConfirmed || saved.CommitUncertain || saved.Applied != 1 || len(saved.Outcomes) != 1 || saved.NativeJSON == "" || !saved.RefreshRequired {
		t.Fatal("original typed transaction not confirmed", err)
	}
	reloaded, err := r.RowReader(ctx, s.ProfileID(), "PUBLIC", object.Name, ref)
	if err != nil || reloaded.Values["note"].Text != update.Values["note"].Text || reloaded.Values["amount"].Text != "2.12345678901234567890" || string(reloaded.Values["bytes"].Bytes) != string([]byte{0, 255, 1}) {
		t.Fatal("saved literal changed or escaped its statement", err)
	}
	_, err = r.Saver(ctx, s.ProfileID(), "PUBLIC", object.Name, []contract.DataEdit{update})
	if !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("stale complete values admitted", err)
	}
	rollback := contract.DataEdit{Kind: "update", Original: reloaded.Snapshot, Values: map[string]contract.DataValue{"note": {Text: "must roll back"}}}
	rolledBack, err := r.Saver(ctx, s.ProfileID(), "PUBLIC", object.Name, []contract.DataEdit{rollback, rollback})
	if !errors.Is(err, contract.ErrDataConflict) || rolledBack.CommitUncertain || rolledBack.CommitConfirmed || rolledBack.Applied != 0 {
		t.Fatal("original batch rollback not acknowledged", err)
	}
	reloaded, err = r.RowReader(ctx, s.ProfileID(), "PUBLIC", object.Name, ref)
	if err != nil || reloaded.Values["note"].Text != update.Values["note"].Text {
		t.Fatal("failed batch leaked its first write", err)
	}
	_, err = r.Saver(ctx, s.ProfileID(), "PUBLIC", object.Name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "fingerprint saved"}}}})
	if err != nil {
		t.Fatal("fingerprint conditional update failed", err)
	}
	// Restore this row so the surrounding original query assertion remains exact.
	_, err = lease.Execute(ctx, "PUBLIC", `UPDATE "quoted;records" SET "note"=NULL WHERE "id"=9007199254740993`)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := r.Saver(ctx, s.ProfileID(), "PUBLIC", object.Name, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "9007199254740995"}, "note": {Null: true}}}})
	if err != nil || !inserted.CommitConfirmed {
		t.Fatal("native insert failed", err)
	}
	count, err := r.Count(ctx, s.ProfileID(), "PUBLIC", object.Name)
	if err != nil || count != 3 {
		t.Fatal("native count changed", err)
	}
	last, err := r.Loader(ctx, s.ProfileID(), "PUBLIC", object.Name, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := r.Saver(ctx, s.ProfileID(), "PUBLIC", object.Name, []contract.DataEdit{{Kind: "delete", Original: last.RowSnapshots[0]}})
	if err != nil || !deleted.CommitConfirmed {
		t.Fatal("original conditional delete failed", err)
	}
	var exported [][]contract.DataValue
	err = r.Exporter(ctx, s.ProfileID(), "PUBLIC", object.Name, contract.DataOptions{Object: object}, []string{"bytes", "amount", "id", "note"}, func(values []contract.DataValue) error {
		exported = append(exported, values)
		return nil
	})
	if err != nil || len(exported) != 2 || exported[0][0].Kind != contract.Binary || exported[0][1].Text != "123456789012345678.12345678901234567890" || exported[0][2].Text != "9007199254740993" || !exported[0][3].Null {
		t.Fatal("complete original cursor export lost types or requested column order", err)
	}
	var jsonl strings.Builder
	if err = r.JSONL(ctx, object, &jsonl); err != nil {
		t.Fatal("native JSONL export failed", err)
	}
	lines := strings.Split(strings.TrimSpace(jsonl.String()), "\n")
	var first []contract.DataValue
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || len(first) != 4 || first[0].Text != "9007199254740993" || first[2].Kind != contract.Binary || !first[3].Null {
		t.Fatal("typed JSONL did not retain complete native values")
	}
	sinkErr := errors.New("original export sink failed")
	if err = r.Exporter(ctx, s.ProfileID(), "PUBLIC", object.Name, contract.DataOptions{Object: object}, nil, func([]contract.DataValue) error { return sinkErr }); !errors.Is(err, sinkErr) {
		t.Fatal("original export sink failure lost", err)
	}
	tooLong := strings.Repeat("z", contract.DataEditMaxBytes+1)
	_, err = lease.Execute(ctx, "PUBLIC", `UPDATE "quoted;records" SET "note"=`+textLiteral(tooLong)+` WHERE "id"=9007199254740994`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RowReader(ctx, s.ProfileID(), "PUBLIC", object.Name, ref)
	if !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("full-row byte limit not enforced", err)
	}
	if err = r.Exporter(ctx, s.ProfileID(), "PUBLIC", object.Name, contract.DataOptions{Object: object}, nil, func([]contract.DataValue) error { return nil }); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("export silently shortened an oversized native field", err)
	}
	exported = nil
	if err = r.Exporter(ctx, s.ProfileID(), "PUBLIC", object.Name, contract.DataOptions{Object: object}, []string{"id"}, func(values []contract.DataValue) error { exported = append(exported, values); return nil }); err != nil || len(exported) != 2 {
		t.Fatal("selected bounded fields were blocked by unselected oversized value", err)
	}
	bad := ref
	bad.Object.ProfileID++
	if _, err = r.RowReader(ctx, s.ProfileID(), "PUBLIC", object.Name, bad); err == nil {
		t.Fatal("foreign original row identity admitted")
	}
}

func testH2NativeTypesAndReadOnlyKeys(t *testing.T, ctx context.Context, s *Session, lease contract.QueryLease) {
	t.Helper()
	_, err := lease.Execute(ctx, "PUBLIC", `CREATE TABLE "native-types"("id" BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,"fixed" CHAR(5),"when" TIMESTAMP(9),"bytes" BINARY(4),"note" CLOB); INSERT INTO "native-types"("fixed","when","bytes","note") VALUES('a',TIMESTAMP '2026-11-15 12:34:56.123456789',X'00FF','original'); CREATE TABLE "no-key"("note" VARCHAR); CREATE TABLE "nullable-key"("id" INTEGER UNIQUE,"note" VARCHAR)`)
	if err != nil {
		t.Fatal(err)
	}
	r := contract.ComposeData(s)
	_, err = lease.Execute(ctx, "PUBLIC", `CREATE TABLE "native-index-order"("a" INTEGER NOT NULL,"b" INTEGER NOT NULL,"note" VARCHAR,PRIMARY KEY("b","a")); CREATE UNIQUE INDEX "different-order" ON "native-index-order"("a","b"); INSERT INTO "native-index-order" VALUES(1,2,'original')`)
	if err != nil {
		t.Fatal(err)
	}
	indexStructure, err := r.TableMetadata(ctx, s.ProfileID(), "PUBLIC", "native-index-order")
	if err != nil {
		t.Fatal(err)
	}
	primaryFound := false
	for _, index := range indexStructure.Indexes {
		if index.Type == "PRIMARY KEY" {
			primaryFound = len(index.Columns) == 2 && index.Columns[0].Name == "b" && index.Columns[0].Position == 1 && index.Columns[1].Name == "a" && index.Columns[1].Position == 2
		}
	}
	if !primaryFound {
		t.Fatal("native composite index order changed")
	}
	indexPage, err := r.Loader(ctx, s.ProfileID(), "PUBLIC", "native-index-order", 0, 1)
	if err != nil || indexPage.RowRefs[0] == nil || strings.Join(indexPage.RowRefs[0].Columns, ",") != "b,a" {
		t.Fatal("native primary key order was replaced by another unique index", err)
	}
	page, err := r.Loader(ctx, s.ProfileID(), "PUBLIC", "native-types", 0, 1)
	if err != nil || page.RowRefs[0] == nil || !*page.Editable || !page.ColumnMetadata[0].Generated {
		t.Fatal("generated identity ceased to be a complete key", err)
	}
	row, err := r.RowReader(ctx, s.ProfileID(), "PUBLIC", "native-types", *page.RowRefs[0])
	fixed := "a    "
	if strings.HasPrefix(s.version, "1.") {
		fixed = "a"
	}
	if err != nil || row.Values["fixed"].Text != fixed || row.Values["when"].Text != "2026-11-15 12:34:56.123456789" {
		t.Fatal("fixed text or nanosecond timestamp changed", err)
	}
	saved, err := r.Saver(ctx, s.ProfileID(), "PUBLIC", "native-types", []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"note": {Text: "changed"}}}})
	if err != nil || !saved.CommitConfirmed {
		t.Fatal("complete typed snapshot could not conditionally update", err)
	}
	row, err = r.RowReader(ctx, s.ProfileID(), "PUBLIC", "native-types", *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	saved, err = r.Saver(ctx, s.ProfileID(), "PUBLIC", "native-types", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"note": {Text: "generated identity"}}}})
	if err != nil || !saved.CommitConfirmed {
		t.Fatal("native generated identity insert failed", err)
	}
	saved, err = r.Saver(ctx, s.ProfileID(), "PUBLIC", "native-types", []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"note": {Text: "after unrelated identity allocation"}}}})
	if err != nil || !saved.CommitConfirmed {
		t.Fatal("identity allocation invalidated an unchanged original row", err)
	}
	for _, name := range []string{"no-key", "nullable-key"} {
		structure, err := r.TableMetadata(ctx, s.ProfileID(), "PUBLIC", name)
		if err != nil || structure.Editable {
			t.Fatal("incomplete or nullable identity was editable", err)
		}
		if _, err = r.Saver(ctx, s.ProfileID(), "PUBLIC", name, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"note": {Text: "must reject"}}}}); !errors.Is(err, contract.ErrDataReadOnly) {
			t.Fatal("table without complete identity admitted shared-grid write", err)
		}
	}
	if !strings.HasPrefix(s.version, "1.") {
		_, err = lease.Execute(ctx, "PUBLIC", `CREATE TABLE "native-json"("id" INTEGER PRIMARY KEY,"payload" JSON); INSERT INTO "native-json" VALUES(1,JSON '{"n":9007199254740993}')`)
		if err != nil {
			t.Fatal(err)
		}
		page, err = r.Loader(ctx, s.ProfileID(), "PUBLIC", "native-json", 0, 1)
		if err != nil || page.Rows[0][1].Value.Kind != contract.JSON {
			t.Fatal("native JSON type lost", err)
		}
		row, err = r.RowReader(ctx, s.ProfileID(), "PUBLIC", "native-json", *page.RowRefs[0])
		if err != nil {
			t.Fatal(err)
		}
		saved, err = r.Saver(ctx, s.ProfileID(), "PUBLIC", "native-json", []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"payload": {Kind: contract.JSON, Text: `{"n":9007199254740994}`}}}})
		if err != nil || !saved.CommitConfirmed {
			t.Fatal("native JSON literal/snapshot changed", err)
		}
	}
}
