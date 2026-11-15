package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type schemaBatch struct {
	statements []string
	guards     map[int]bool
	catalog    int
	rows       int
	identities int
}

func schemaWriteBounds(original contract.SchemaDesignSnapshot, desired *schemaddl.Table) error {
	if original.Schema.Engine != "sqlite" || original.Schema.SQLite == nil || original.Schema.SQLite.Validate() != nil || original.Schema.Postgres != nil || original.Schema.OpenGauss != nil {
		return connections.ErrInvalid
	}
	for _, t := range []*schemaddl.Table{&original.Schema.Table, desired} {
		if t == nil {
			continue
		}
		if len(t.Clauses) > 3000 || len(t.Definitions) > 1000 || t.Postgres != nil || t.OpenGauss != nil {
			return contract.ErrUnsupported
		}
		columns := len(t.Columns())
		if columns > 2000 || len(t.Clauses)-columns > 1000 {
			return contract.ErrUnsupported
		}
		size := len(t.Name) + len(t.Options)
		for _, c := range t.Clauses {
			size += len(c.ID) + len(c.Name) + len(c.SQL)
			if c.Postgres != nil || c.OpenGauss != nil {
				return contract.ErrUnsupported
			}
		}
		for _, d := range t.Definitions {
			size += len(d.Name) + len(d.SQL)
			if d.Postgres != nil || d.OpenGauss != nil {
				return contract.ErrUnsupported
			}
		}
		if size > 16<<20 {
			return contract.ErrUnsupported
		}
	}
	return nil
}

func (d *schemaDocument) writable(target contract.SchemaDesignTarget) bool {
	d.mu.Lock()
	ready := !d.closed && !d.blocked && d.target == target
	d.mu.Unlock()
	return ready && !d.s.config.Options.ReadOnly && d.s.checkSchemaTarget(target) == nil
}

func (d *schemaDocument) plan(request contract.SchemaDesignRequest) (schemaddl.Plan, error) {
	if err := schemaWriteBounds(request.Original, &request.Desired); err != nil {
		return schemaddl.Plan{}, err
	}
	r := request.Clone()
	ctx, scope, done, err := d.admission(context.Background(), r.Original.Target)
	if err != nil {
		return schemaddl.Plan{}, err
	}
	defer done()
	if d.s.config.Options.ReadOnly {
		return schemaddl.Plan{}, contract.ErrUnsupported
	}
	d.mu.Lock()
	blocked := d.blocked
	d.mu.Unlock()
	if blocked {
		return schemaddl.Plan{}, contract.ErrSchemaCommitUncertain
	}
	if ctx.Err() != nil {
		return schemaddl.Plan{}, ctx.Err()
	}
	if _, err := d.s.schemaSnapshotGuardScoped(r.Original, scope); err != nil {
		return schemaddl.Plan{}, err
	}
	target := r.Original.Target
	target.Object.Name = r.Desired.Name
	if err := d.s.checkSchemaTarget(target); err != nil {
		return schemaddl.Plan{}, err
	}
	return schemaddl.PlanSQLiteNative(r.Original.Schema, r.Desired, "main", r.AllowColumnDrop)
}
func (d *schemaDocument) apply(ctx context.Context, request contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
	if err := schemaWriteBounds(request.Original, &request.Desired); err != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
	}
	r := request.Clone()
	receipt := contract.SchemaDesignWriteReceipt{Operation: "apply", Original: r.Original, Desired: &r.Desired, Destination: r.Desired.Name, AllowColumnDrop: r.AllowColumnDrop}
	return d.write(ctx, receipt)
}
func (d *schemaDocument) rename(ctx context.Context, request contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
	if err := schemaWriteBounds(request.Original, nil); err != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
	}
	r := request.Clone()
	return d.write(ctx, contract.SchemaDesignWriteReceipt{Operation: "rename", Original: r.Original, Destination: r.Destination})
}
func (d *schemaDocument) drop(ctx context.Context, original contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
	if err := schemaWriteBounds(original, nil); err != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
	}
	r := original.Clone()
	return d.write(ctx, contract.SchemaDesignWriteReceipt{Operation: "drop", Original: r, Destination: r.Target.Object.Name})
}

func (d *schemaDocument) copy(ctx context.Context, request contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
	if err := schemaWriteBounds(request.Original, nil); err != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
	}
	r := request.Clone()
	return d.write(ctx, contract.SchemaDesignWriteReceipt{Operation: "copy", Original: r.Original, Destination: r.Destination, WithData: r.WithData, RebindSelfReferences: r.RebindSelfReferences, PreserveRowID: r.PreserveRowID})
}

func (d *schemaDocument) clear(ctx context.Context, request contract.SchemaClearRequest) contract.SchemaDesignOutcome {
	if err := schemaWriteBounds(request.Original, nil); err != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
	}
	r := request.Clone()
	return d.write(ctx, contract.SchemaDesignWriteReceipt{Operation: "clear", Original: r.Original, Destination: r.Original.Target.Object.Name, ResetIdentity: r.ResetIdentity})
}

func (d *schemaDocument) write(ctx context.Context, r contract.SchemaDesignWriteReceipt) contract.SchemaDesignOutcome {
	child, scope, done, err := d.admission(ctx, r.Original.Target)
	if err != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
	}
	defer done()
	select {
	case <-child.Done():
		return contract.SchemaDesignOutcome{State: "not_executed", Error: child.Err()}
	case d.writes <- struct{}{}:
	}
	defer func() { <-d.writes }()
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrRevoked}
	}
	if d.blocked {
		d.mu.Unlock()
		return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrSchemaCommitUncertain}
	}
	if r.Original.Target != d.target {
		d.mu.Unlock()
		return contract.SchemaDesignOutcome{State: "not_executed", Error: connections.ErrInvalid}
	}
	d.sequence++
	r.Sequence, r.DocumentID = d.sequence, d.id
	d.mu.Unlock()
	if child.Err() != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: child.Err()}
	}
	out := d.executeSchema(child, scope, r)
	d.mu.Lock()
	defer d.mu.Unlock()
	if out.Receipt != nil {
		original := out.Clone()
		d.original = &original
	}
	if out.State == "uncertain" || out.State == "committed" && (!out.Verified || out.Error != nil) {
		d.blocked = true
	}
	if r.Operation != "copy" && r.Operation != "clear" && !d.closed && out.State == "committed" && out.Verified && out.Error == nil && out.Actual != nil {
		d.target = out.Actual.Target
	}
	// A dispatched write returns its complete original settlement even after
	// caller/document/session cancellation. Later reads never reclassify it.
	return out
}

func (d *schemaDocument) executeSchema(ctx context.Context, scope string, r contract.SchemaDesignWriteReceipt) contract.SchemaDesignOutcome {
	out := contract.SchemaDesignOutcome{State: "not_executed"}
	if d.s.config.Options.ReadOnly {
		out.Error = contract.ErrUnsupported
		return out
	}
	original := r.Original
	guard, err := d.s.schemaSnapshotGuardScoped(original, scope)
	if err != nil {
		out.Error = err
		return out
	}
	if original.Schema.Table.Name != original.Target.Object.Name {
		out.Error = connections.ErrInvalid
		return out
	}
	target := original.Target
	target.Object.Name = r.Destination
	if err := d.s.checkSchemaTarget(target); err != nil {
		out.Error = err
		return out
	}
	var desired schemaddl.Table
	var plan schemaddl.Plan
	var copyProgram *schemaCopyProgram
	var clearProgram *schemaClearProgram
	switch r.Operation {
	case "apply":
		desired = r.Desired.Clone()
		plan, err = schemaddl.PlanSQLiteNative(original.Schema, desired, "main", r.AllowColumnDrop)
	case "rename":
		if !original.Schema.Exists {
			out.Error = connections.ErrInvalid
			return out
		}
		desired = original.Schema.Table.Clone()
		desired.Name = r.Destination
		plan, err = schemaddl.PlanSQLiteNative(original.Schema, desired, "main", false)
	case "drop":
		if !original.Schema.Exists {
			out.Error = connections.ErrInvalid
			return out
		}
		plan = schemaddl.Plan{Mode: "drop", SQL: []string{"DROP TABLE main." + schemaddl.Quote(original.Target.Object.Name)}}
	case "copy":
		if !original.Schema.Exists {
			out.Error = connections.ErrInvalid
			return out
		}
		desired, err = schemaddl.CopySQLite(original.Schema.Table, "main", r.Destination, r.RebindSelfReferences)
		if err == nil {
			plan.Mode = "copy"
			plan.SQL, err = schemaddl.CopySQLiteSQL(desired, "main")
		}
		if err == nil {
			err = schemaWriteBounds(original, &desired)
		}
		if err == nil {
			copyProgram, err = buildSchemaCopyProgram(original, desired, r, scope)
		}
	case "clear":
		if !original.Schema.Exists {
			out.Error = connections.ErrInvalid
			return out
		}
		desired = original.Schema.Table.Clone()
		clearProgram, plan, err = buildSchemaClearProgram(original, r.ResetIdentity)
	default:
		err = contract.ErrUnsupported
	}
	if err != nil {
		out.Error = err
		return out
	}
	if plan.Mode == "noop" {
		return out
	} // No transaction or synthetic commit.
	batch, err := schemaWriteBatch(original, desired, plan, guard, scope, r.Sequence, copyProgram, clearProgram)
	if err != nil {
		out.Error = err
		return out
	}
	r.Statements = append([]string(nil), batch.statements...)
	statements := make([]any, len(batch.statements))
	for i, sql := range batch.statements {
		statements[i] = sql
	}
	// /db/request in v8 skips preparation failures without aborting its
	// transaction. /db/execute aborts these execution failures; RETURNING
	// retains the final catalog in the same original transaction.
	native, requestErr := d.s.request(ctx, "/db/execute", statements, true, true)
	r.NativeJSON = native.Raw
	finish := func() contract.SchemaDesignOutcome {
		r.State, r.Verified, r.Rows = out.State, out.Verified, out.Rows
		if out.Actual != nil {
			a := out.Actual.Clone()
			r.Actual = &a
		}
		out.Receipt = &r
		return out
	}
	if native.Uncertain || errors.Is(requestErr, errIncomplete) || errors.Is(requestErr, errResponseLost) {
		out.State = "uncertain"
		out.Error = errors.Join(contract.ErrSchemaCommitUncertain, requestErr)
		return finish()
	}
	if requestErr != nil {
		out.Error = requestErr
		for i, result := range native.Results {
			if result.failed() {
				out.State = "rolled_back"
				if batch.guards[i] && guardRejected(result) {
					out.Error = errors.Join(schemaddl.ErrConflict, requestErr)
				}
				break
			}
		}
		return finish() // Complete auth/parser rejection remains not_executed.
	}
	out.State = "committed"
	unverified := func(cause error) contract.SchemaDesignOutcome {
		out.Error = errors.Join(contract.ErrSchemaCommitUncertain, cause)
		out.Verified = false
		return finish()
	}
	if len(native.Results) != len(batch.statements) {
		return unverified(errIncomplete)
	}
	for i := range batch.guards {
		n, err := native.Results[i].Affected.Int64()
		if err != nil || n != 1 {
			return unverified(errIncomplete)
		}
	}
	rows, err := decodedRows(native, batch.rows, 1)
	if err != nil || len(rows) != 1 {
		return unverified(errIncomplete)
	}
	out.Rows, err = cellInt(rows[0][0])
	if err != nil || out.Rows < 0 {
		return unverified(errIncomplete)
	}
	actual, err := d.s.decodeSchemaSnapshot(target, native, batch.catalog)
	if err != nil {
		return unverified(err)
	}
	actual.Schema.Revision = schemaSnapshotSealScoped(actual, scope)
	out.Actual = &actual // Keep the complete captured catalog even if verification fails.
	if clearProgram != nil {
		if actual.Schema.SQLite.Version != original.Schema.SQLite.Version {
			return unverified(schemaddl.ErrConflict)
		}
		if err := verifySchemaClearIdentities(native, batch.identities); err != nil {
			return unverified(err)
		}
	}
	if plan.Mode == "drop" {
		err = schemaddl.VerifySQLiteNativeDrop(original.Schema, actual.Schema)
	} else if plan.Mode == "copy" {
		// Creation verification keeps every original catalog row, including the
		// complete source and all unselected objects, and admits only the copy.
		err = schemaddl.VerifySQLiteNativeCatalog(original.Schema, desired, actual.Schema, schemaddl.Plan{Mode: "create"})
	} else {
		err = schemaddl.VerifySQLiteNativeCatalog(original.Schema, desired, actual.Schema, plan)
	}
	if err != nil {
		return unverified(err)
	}
	for _, definition := range actual.Schema.Table.Definitions {
		if err := schemaddl.ValidateSQLiteTriggerColumns(definition, actual.Schema.Table); err != nil {
			return unverified(err)
		}
	}
	out.Actual, out.Verified = &actual, true
	return finish()
}

func schemaWriteBatch(original contract.SchemaDesignSnapshot, desired schemaddl.Table, plan schemaddl.Plan, guard, scope string, sequence uint64, copyProgram *schemaCopyProgram, clearProgram *schemaClearProgram) (schemaBatch, error) {
	b := schemaBatch{guards: map[int]bool{}}
	name := schemaddl.Quote("__nyadb_schema_" + scope[:16] + fmt.Sprintf("_%d", sequence))
	table := "temp." + name
	identityColumns := ""
	if clearProgram != nil {
		identityColumns = ",identity_phase TEXT,identity_count INTEGER,identity_rowid INTEGER,identity_name_type TEXT,identity_name_hex TEXT,identity_value_type TEXT,identity_value_hex TEXT"
	}
	b.statements = append(b.statements, "CREATE TEMP TABLE "+table+"(ok INTEGER NOT NULL CHECK(ok=1),original_rows INTEGER,schema_version INTEGER,type_hex TEXT,name_hex TEXT,table_hex TEXT,rootpage INTEGER,sql_hex TEXT,sql_null INTEGER"+identityColumns+")")
	addGuard := func(condition string) {
		b.guards[len(b.statements)] = true
		b.statements = append(b.statements, "INSERT INTO "+table+"(ok) SELECT ("+condition+")")
	}
	count := "0"
	if original.Schema.Exists && (copyProgram == nil || copyProgram.withData) {
		count = "(SELECT count(*) FROM main." + schemaddl.Quote(original.Target.Object.Name) + ")"
	}
	b.guards[len(b.statements)] = true
	b.statements = append(b.statements, "INSERT INTO "+table+"(ok,original_rows) SELECT ("+guard+"),"+count)
	addGuard("NOT EXISTS(SELECT 1 FROM temp.sqlite_schema WHERE type IN ('view','trigger'))")
	if plan.Mode == "rename_column" || plan.Mode == "rename_table" {
		addGuard("(SELECT legacy_alter_table FROM pragma_legacy_alter_table)=0")
	}
	addGuard("NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check)")
	if plan.Mode == "drop" {
		source := schemaTextLiteral(original.Target.Object.Name)
		addGuard("NOT EXISTS(SELECT 1 FROM main.sqlite_schema t JOIN pragma_foreign_key_list(t.name) f ON 1=1 WHERE t.type='table' AND t.name COLLATE NOCASE<>" + source + " AND f.\"table\" COLLATE NOCASE=" + source + ")")
	}
	before, err := schemaProgramChecks(original.Schema.SQLite, original.Schema.Table, nil, schemaddl.Plan{})
	if err != nil {
		return b, err
	}
	for _, sql := range before {
		b.statements = append(b.statements, sql)
	}
	if clearProgram != nil {
		for _, condition := range clearProgram.beforeGuards {
			addGuard(condition)
		}
		b.statements = append(b.statements, clearProgram.capture(table, "before")...)
	}
	if copyProgram == nil {
		b.statements = append(b.statements, plan.SQL...)
	} else {
		for _, condition := range copyProgram.beforeGuards {
			addGuard(condition)
		}
		b.statements = append(b.statements, copyProgram.beforeSQL...)
		b.statements = append(b.statements, plan.SQL[0])
		b.statements = append(b.statements, copyProgram.dataSQL...)
		// Install indexes and trigger programs only after the original data copy.
		b.statements = append(b.statements, plan.SQL[1:]...)
	}
	after, err := schemaProgramChecks(original.Schema.SQLite, original.Schema.Table, &desired, plan)
	if err != nil {
		return b, err
	}
	for _, sql := range after {
		b.statements = append(b.statements, sql)
	}
	addGuard("NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check)")
	if clearProgram != nil {
		for _, condition := range clearProgram.afterGuards {
			addGuard(condition)
		}
		b.statements = append(b.statements, clearProgram.capture(table, "after")...)
		b.identities = len(b.statements)
		b.statements = append(b.statements, "DELETE FROM "+name+" WHERE identity_phase IS NOT NULL RETURNING identity_phase,identity_count,identity_rowid,identity_name_type,identity_name_hex,identity_value_type,identity_value_hex")
	}
	if copyProgram != nil {
		for _, condition := range copyProgram.afterGuards {
			addGuard(condition)
		}
		b.statements = append(b.statements, copyProgram.cleanupSQL...)
	}
	if plan.Mode == "drop" {
		addGuard("NOT EXISTS(SELECT 1 FROM main.sqlite_schema WHERE type IN ('table','view') AND name COLLATE NOCASE=" + schemaTextLiteral(original.Target.Object.Name) + ")")
	} else if clearProgram != nil {
		addGuard("(SELECT count(*) FROM main." + schemaddl.Quote(desired.Name) + ")=0")
	} else {
		addGuard("(SELECT count(*) FROM main." + schemaddl.Quote(desired.Name) + ")=(SELECT original_rows FROM " + table + " WHERE original_rows IS NOT NULL LIMIT 1)")
	}
	b.rows = len(b.statements)
	b.statements = append(b.statements, "DELETE FROM "+name+" WHERE original_rows IS NOT NULL RETURNING original_rows")
	b.statements = append(b.statements, "INSERT INTO "+table+"(ok,schema_version) SELECT 1,schema_version FROM pragma_schema_version")
	b.statements = append(b.statements, "INSERT INTO "+table+"(ok,type_hex,name_hex,table_hex,rootpage,sql_hex,sql_null) SELECT 1,hex(type),hex(name),hex(tbl_name),rootpage,hex(coalesce(sql,'')),sql IS NULL FROM main.sqlite_schema ORDER BY type COLLATE BINARY,name COLLATE BINARY")
	b.catalog = len(b.statements)
	b.statements = append(b.statements,
		"DELETE FROM "+name+" WHERE schema_version IS NOT NULL RETURNING schema_version",
		"DELETE FROM "+name+" WHERE type_hex IS NOT NULL RETURNING type_hex,name_hex,table_hex,rootpage,sql_hex,sql_null",
		"DROP TABLE "+table)
	raw, err := json.Marshal(b.statements)
	if err != nil || len(raw) > maxRequestBytes || len(b.statements) > 1024 {
		return schemaBatch{}, errors.Join(contract.ErrUnsupported, errors.New("complete guarded schema request exceeds native bounds"))
	}
	return b, nil
}

func schemaProgramChecks(catalog *schemaddl.SQLiteCatalog, original schemaddl.Table, desired *schemaddl.Table, plan schemaddl.Plan) ([]string, error) {
	tables := map[string]schemaddl.Table{}
	triggerTables := map[string]bool{}
	for _, e := range catalog.Entries {
		if e.Type == "trigger" {
			triggerTables[e.Table] = true
		}
	}
	var sql []string
	for _, e := range catalog.Entries {
		if e.Type == "view" {
			sql = append(sql, "EXPLAIN SELECT * FROM main."+schemaddl.Quote(e.Name)+" LIMIT 0")
		}
		if e.Type != "table" || !triggerTables[e.Name] {
			continue
		}
		if desired != nil && plan.Mode != "copy" && e.Name == original.Name {
			if plan.Mode == "drop" {
				continue
			}
			tables[desired.Name] = desired.Clone()
			continue
		}
		t, err := schemaddl.ParseSQLite(e.SQL)
		if err != nil {
			return nil, errors.Join(contract.ErrUnsupported, errors.New("dependent trigger table cannot be represented completely"))
		}
		tables[e.Name] = t
	}
	if original.Name != "" && original.Clauses != nil && desired == nil {
		tables[original.Name] = original.Clone()
	}
	if desired != nil && plan.Mode != "drop" {
		tables[desired.Name] = desired.Clone()
	}
	// Header validation is pure and precedes dispatch; native rename rewrites its
	// selected original trigger header rather than applying caller body edits.
	for _, e := range catalog.Entries {
		if e.Type != "trigger" {
			continue
		}
		if desired != nil && plan.Mode != "copy" && e.Table == original.Name {
			continue
		}
		t, ok := tables[e.Table]
		if !ok {
			return nil, errors.Join(contract.ErrUnsupported, errors.New("dependent view trigger requires native program validation"))
		}
		if err := schemaddl.ValidateSQLiteTriggerColumns(schemaddl.Definition{Kind: "trigger", Name: e.Name, SQL: e.SQL}, t); err != nil {
			return nil, err
		}
	}
	if desired != nil && plan.Mode != "drop" && plan.Mode != "rename_column" && plan.Mode != "rename_table" {
		for _, d := range desired.Definitions {
			if err := schemaddl.ValidateSQLiteTriggerColumns(d, *desired); err != nil {
				return nil, err
			}
		}
	}
	// Catalog order makes exact original request arrays deterministic.
	names := []string{}
	for _, e := range catalog.Entries {
		if e.Type == "table" {
			name := e.Name
			if desired != nil && plan.Mode != "copy" && name == original.Name {
				name = desired.Name
			}
			if _, ok := tables[name]; ok {
				names = append(names, name)
				delete(triggerTables, name)
			}
		}
	}
	// New tables have no original catalog row.
	found := false
	for _, name := range names {
		if desired != nil && name == desired.Name {
			found = true
		}
	}
	if desired != nil && plan.Mode != "drop" && !found {
		names = append(names, desired.Name)
	}
	for _, name := range names {
		t := tables[name]
		columns, err := schemaddl.SQLiteWritableColumns(t)
		if err != nil {
			return nil, err
		}
		target := "main." + schemaddl.Quote(name)
		var columnNames, nulls []string
		for _, c := range columns {
			columnNames = append(columnNames, schemaddl.Quote(c.Name))
			nulls = append(nulls, "NULL")
		}
		if len(columns) == 0 {
			return nil, contract.ErrUnsupported
		}
		// /db/execute prepares EXPLAIN with ExecContext and aborts preparation
		// failures. It does not run triggers, rows, defaults or sqlite_sequence.
		sql = append(sql, "EXPLAIN INSERT INTO "+target+" ("+strings.Join(columnNames, ",")+") SELECT "+strings.Join(nulls, ",")+" WHERE 0", "EXPLAIN DELETE FROM "+target+" WHERE 0")
		var updates []string
		for _, c := range columns {
			updates = append(updates, schemaddl.Quote(c.Name)+"="+schemaddl.Quote(c.Name))
		}
		if len(updates) > 0 {
			sql = append(sql, "EXPLAIN UPDATE "+target+" SET "+strings.Join(updates, ",")+" WHERE 0")
		}
	}
	return sql, nil
}
