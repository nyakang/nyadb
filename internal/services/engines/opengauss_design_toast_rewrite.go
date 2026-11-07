package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// A fixed-width final CREATE need not have TOAST, while ALTER on the original
// heap can rebuild its existing TOAST tree. Observe that native reconstruction
// on a separate empty carrier, never on the original or its row values.
type openGaussToastRewriteProof struct {
	Request, Catalog, BeforeRevision, BeforeSignature string
	Before                                            *schemaddl.OpenGaussCatalog
	Columns                                           map[string]openGaussToastRewriteObservation
}

type openGaussToastRewriteObservation struct {
	Column, Revision, Signature string
	After                       *schemaddl.OpenGaussCatalog
}

var errOpenGaussNotOrdinaryHeap = errors.New("native storage probe requires an ordinary heap")

func openGaussToastGeneratedColumns(original contract.SchemaDesignSnapshot, desired schemaddl.Table) ([]schemaddl.Clause, error) {
	old := map[string]bool{}
	for _, col := range original.Schema.Table.Columns() {
		old[col.ID] = true
	}
	var out []schemaddl.Clause
	for _, col := range desired.Columns() {
		if old[col.ID] {
			continue
		}
		f, err := schemaddl.ParseOpenGaussField(col.SQL)
		if err != nil {
			return nil, err
		}
		if f.Generation != "" {
			out = append(out, col)
		}
	}
	return out, nil
}

func openGaussToastDropsBeforeAdd(original contract.SchemaDesignSnapshot, desired schemaddl.Table) bool {
	old := map[string]bool{}
	for _, col := range original.Schema.Table.Columns() {
		old[col.ID] = true
	}
	for _, col := range desired.Columns() {
		if old[col.ID] {
			return true // PlanOpenGauss drops removed fields before adding new ones.
		}
	}
	return false
}

func openGaussHeapStorage(catalog *schemaddl.OpenGaussCatalog) (file, toast uint64, err error) {
	if catalog == nil || catalog.RelationOID == 0 {
		return 0, 0, schemaddl.ErrConflict
	}
	for _, entry := range catalog.Entries {
		if entry.Kind != "relation" || entry.OID != catalog.RelationOID {
			continue
		}
		var row struct {
			File       uint64 `json:"relfilenode"`
			Toast      uint64 `json:"reltoastrelid"`
			Kind, Part string
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(entry.Data, &row) != nil || json.Unmarshal(entry.Data, &data) != nil || json.Unmarshal(data["relkind"], &row.Kind) != nil || json.Unmarshal(data["parttype"], &row.Part) != nil {
			return 0, 0, schemaddl.ErrConflict
		}
		if row.Kind != "r" || row.Part != "n" {
			return 0, 0, errOpenGaussNotOrdinaryHeap
		}
		if row.File == 0 {
			return 0, 0, schemaddl.ErrConflict
		}
		return row.File, row.Toast, nil
	}
	return 0, 0, schemaddl.ErrConflict
}

func openGaussHeapOwner(catalog *schemaddl.OpenGaussCatalog) (uint64, error) {
	if catalog == nil {
		return 0, schemaddl.ErrConflict
	}
	for _, entry := range catalog.Entries {
		if entry.Kind == "relation" && entry.OID == catalog.RelationOID {
			var row struct {
				Owner uint64 `json:"relowner"`
			}
			if json.Unmarshal(entry.Data, &row) != nil || row.Owner == 0 {
				return 0, schemaddl.ErrConflict
			}
			return row.Owner, nil
		}
	}
	return 0, schemaddl.ErrConflict
}

func openGaussCaptureToastRewrite(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, baseline openGaussDesignBaseline) (out *openGaussToastRewriteProof, err error) {
	if !r.Original.Schema.Exists || baseline.Catalog == nil {
		return nil, nil
	}
	cols, err := openGaussToastGeneratedColumns(r.Original, r.Desired)
	if err != nil || len(cols) == 0 {
		return nil, err
	}
	_, originalToast, err := openGaussHeapStorage(r.Original.Schema.OpenGauss)
	if errors.Is(err, errOpenGaussNotOrdinaryHeap) {
		// Partition/column storage has its own identity verification paths.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, declaredToast, err := openGaussHeapStorage(baseline.Catalog)
	if err != nil || originalToast == 0 || declaredToast != 0 {
		return nil, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	name := "__nyadb_gauss_storage_" + hex.EncodeToString(random[:])
	carrier := baseline.Table.Clone()
	carrier.Name, carrier.Definitions = name, nil
	carrier.OpenGauss = r.Original.Schema.Table.OpenGauss.Clone()
	carrier.OpenGauss.Owner, carrier.OpenGauss.Comment = "", nil
	carrier.Clauses = nil
	// Preserve only independently parsed field types for expression binding.
	// No original/generated/default expression or owned sequence is executed by
	// CREATE. A private text field forces the carrier's native TOAST tree.
	for i, col := range baseline.Table.Columns() {
		f, e := schemaddl.ParseOpenGaussField(col.SQL)
		if e != nil {
			return nil, e
		}
		f.NotNull, f.HasDefault, f.Default, f.Generation, f.GenerationStorage = false, false, "", "", ""
		body, e := f.SQL()
		if e != nil {
			return nil, e
		}
		carrier.Clauses = append(carrier.Clauses, schemaddl.Clause{ID: "carrier:" + strconv.Itoa(i), Kind: "column", Name: col.Name, SQL: body})
	}
	payload := "__nyadb_payload_" + hex.EncodeToString(random[:])
	carrier.Clauses = append(carrier.Clauses, schemaddl.Clause{ID: "carrier:payload", Kind: "column", Name: payload, SQL: "text"})
	// Pin the captured heap's default storage when the current session uses a
	// different default for new tables. Native snapshots record explicit Ustore.
	hasStorage := false
	for _, option := range carrier.OpenGauss.Parameters {
		key, _, _ := strings.Cut(option, "=")
		hasStorage = hasStorage || strings.EqualFold(strings.TrimSpace(key), "storage_type")
	}
	if !hasStorage {
		carrier.OpenGauss.Parameters = append(carrier.OpenGauss.Parameters, "storage_type=astore")
	}
	if err = openGaussCheckDesignDeclaration(ctx, c, carrier, r.Original.Target.Object.Schema); err != nil {
		return nil, err
	}
	target := r.Original.Target
	target.Object.Name = name
	absent, err := openGaussReadDesign(ctx, c, target)
	if err != nil || absent.Schema.Exists {
		return nil, errors.Join(err, schemaddl.ErrConflict)
	}
	plan, err := schemaddl.PlanOpenGauss(absent.Schema, carrier, target.Object.Schema, false)
	if err != nil {
		return nil, err
	}
	if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_gauss_toast_carrier"); err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, rollbackErr := c.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_gauss_toast_carrier")
		var releaseErr error
		if rollbackErr == nil {
			_, releaseErr = c.ExecContext(cleanup, "RELEASE SAVEPOINT nyadb_gauss_toast_carrier")
		}
		if rollbackErr != nil || releaseErr != nil {
			discardDesign(c)
			err = errors.Join(err, ErrUncertain, rollbackErr, releaseErr)
		}
		if err != nil {
			out = nil
		}
	}()
	for _, statement := range plan.SQL {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			return nil, err
		}
	}
	// Native index creation binds pg_object.creator to the heap owner. Transfer
	// this empty carrier to the independently captured owner before observing;
	// the authenticated actor and its management permission remain unchanged.
	if _, err = c.ExecContext(ctx, "ALTER TABLE "+qualified(target.Object)+" OWNER TO "+quote(r.Original.Schema.Table.OpenGauss.Owner)); err != nil {
		return nil, err
	}
	before, err := openGaussReadDesign(ctx, c, target)
	if err != nil {
		return nil, err
	}
	var count int64
	if err = c.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(target.Object)).Scan(&count); err != nil || count != 0 {
		return nil, errors.Join(err, schemaddl.ErrConflict)
	}
	canonicalRevision, err := baseline.Catalog.Fingerprint()
	if err != nil {
		return nil, err
	}
	out = &openGaussToastRewriteProof{Request: openGaussDefaultRewriteRequest(r.Original, r.Desired), Catalog: canonicalRevision, BeforeRevision: before.Schema.Revision, BeforeSignature: schemaddl.Signature(before.Schema.OpenGauss), Before: before.Schema.OpenGauss.Clone(), Columns: map[string]openGaussToastRewriteObservation{}}
	for i, col := range cols {
		dropPayload := ""
		if openGaussToastDropsBeforeAdd(r.Original, r.Desired) {
			dropPayload = payload
		}
		observation, e := openGaussObserveToastRewrite(ctx, c, before, carrier, col, "__nyadb_added_"+hex.EncodeToString(random[:])+"_"+strconv.Itoa(i), dropPayload)
		if e != nil {
			return nil, e
		}
		out.Columns[col.ID] = observation
	}
	return out, nil
}

func openGaussObserveToastRewrite(ctx context.Context, c *sql.Conn, before contract.SchemaDesignSnapshot, carrier schemaddl.Table, col schemaddl.Clause, name, dropPayload string) (out openGaussToastRewriteObservation, err error) {
	probe := carrier.Clone()
	added := schemaddl.Clause{ID: "carrier:added", Kind: "column", Name: name, SQL: col.SQL}
	probe.Clauses = append(probe.Clauses, added)
	if err = openGaussCheckDesignDeclaration(ctx, c, probe, before.Target.Object.Schema); err != nil {
		return out, err
	}
	if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_gauss_toast_column"); err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, rollbackErr := c.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_gauss_toast_column")
		var releaseErr error
		if rollbackErr == nil {
			_, releaseErr = c.ExecContext(cleanup, "RELEASE SAVEPOINT nyadb_gauss_toast_column")
		}
		if rollbackErr != nil || releaseErr != nil {
			discardDesign(c)
			err = errors.Join(err, ErrUncertain, rollbackErr, releaseErr)
		} else {
			restored, readErr := openGaussReadDesign(cleanup, c, before.Target)
			if readErr != nil || restored.Schema.Revision != before.Schema.Revision || schemaddl.Signature(restored.Schema.OpenGauss) != schemaddl.Signature(before.Schema.OpenGauss) {
				err = errors.Join(err, readErr, schemaddl.ErrConflict)
			}
		}
		if err != nil {
			out = openGaussToastRewriteObservation{}
		}
	}()
	if dropPayload != "" {
		if _, err = c.ExecContext(ctx, "ALTER TABLE "+qualified(before.Target.Object)+" DROP COLUMN "+quote(dropPayload)+" RESTRICT"); err != nil {
			return out, err
		}
		// DROP alone preserves TOAST. Its removal must be observed during the
		// following native rewrite, not inferred from the fixed-width CREATE.
		dropped, e := openGaussReadDesign(ctx, c, before.Target)
		if e != nil {
			return out, e
		}
		oldFile, oldToast, e := openGaussHeapStorage(before.Schema.OpenGauss)
		if e != nil {
			return out, e
		}
		file, toast, e := openGaussHeapStorage(dropped.Schema.OpenGauss)
		if e != nil || file != oldFile || toast != oldToast {
			return out, errors.Join(e, schemaddl.ErrConflict)
		}
	}
	if _, err = c.ExecContext(ctx, "ALTER TABLE "+qualified(before.Target.Object)+" ADD COLUMN "+quote(name)+" "+col.SQL); err != nil {
		return out, err
	}
	after, err := openGaussReadDesign(ctx, c, before.Target)
	if err != nil {
		return out, err
	}
	var count int64
	if err = c.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(before.Target.Object)).Scan(&count); err != nil || count != 0 {
		return out, errors.Join(err, schemaddl.ErrConflict)
	}
	return openGaussToastRewriteObservation{Column: schemaddl.Signature(col), Revision: after.Schema.Revision, Signature: schemaddl.Signature(after.Schema.OpenGauss), After: after.Schema.OpenGauss.Clone()}, nil
}

func openGaussVerifiedToastRewrite(original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline) (map[string]map[string]any, bool, error) {
	proof := baseline.ToastRewrite
	if proof == nil {
		return nil, false, nil
	}
	cols, err := openGaussToastGeneratedColumns(original, desired)
	if err != nil || baseline.Catalog == nil || proof.Before == nil || len(cols) == 0 || len(cols) != len(proof.Columns) || proof.Request != openGaussDefaultRewriteRequest(original, desired) {
		return nil, false, errors.Join(err, schemaddl.ErrConflict)
	}
	canonicalRevision, err := baseline.Catalog.Fingerprint()
	if err != nil || canonicalRevision != proof.Catalog {
		return nil, false, errors.Join(err, schemaddl.ErrConflict)
	}
	_, originalToast, err := openGaussHeapStorage(original.Schema.OpenGauss)
	if err != nil || originalToast == 0 {
		return nil, false, errors.Join(err, schemaddl.ErrConflict)
	}
	_, declaredToast, err := openGaussHeapStorage(baseline.Catalog)
	if err != nil || declaredToast != 0 {
		return nil, false, errors.Join(err, schemaddl.ErrConflict)
	}
	beforeRevision, err := proof.Before.Fingerprint()
	if err != nil || beforeRevision != proof.BeforeRevision || schemaddl.Signature(proof.Before) != proof.BeforeSignature || original.Schema.OpenGauss == nil || !strings.HasPrefix(proof.Before.Name, "__nyadb_gauss_storage_") || proof.Before.RelationOID == original.Schema.OpenGauss.RelationOID || proof.Before.RelationOID == baseline.Catalog.RelationOID {
		return nil, false, errors.Join(err, schemaddl.ErrConflict)
	}
	beforeFile, beforeToast, err := openGaussHeapStorage(proof.Before)
	if err != nil || beforeToast == 0 {
		return nil, false, errors.Join(err, schemaddl.ErrConflict)
	}
	owner, err := openGaussHeapOwner(original.Schema.OpenGauss)
	if err != nil {
		return nil, false, err
	}
	carrierOwner, err := openGaussHeapOwner(proof.Before)
	if err != nil || carrierOwner != owner {
		return nil, false, errors.Join(err, schemaddl.ErrConflict)
	}
	prototypes := map[string]map[string]any{}
	removed := openGaussToastDropsBeforeAdd(original, desired)
	for _, col := range cols {
		observation, ok := proof.Columns[col.ID]
		if !ok || observation.Column != schemaddl.Signature(col) || observation.After == nil {
			return nil, false, schemaddl.ErrConflict
		}
		after := observation.After
		revision, err := after.Fingerprint()
		if err != nil || revision != observation.Revision || schemaddl.Signature(after) != observation.Signature || after.RelationOID != proof.Before.RelationOID || after.Name != proof.Before.Name || !openGaussToastPrototypeScope(original.Schema.OpenGauss, proof.Before) || !openGaussToastPrototypeScope(original.Schema.OpenGauss, after) {
			return nil, false, errors.Join(err, schemaddl.ErrConflict)
		}
		file, toast, err := openGaussHeapStorage(after)
		if err != nil || file == beforeFile || toast == beforeToast || (toast == 0) != removed {
			return nil, false, errors.Join(err, schemaddl.ErrConflict)
		}
		afterOwner, err := openGaussHeapOwner(after)
		if err != nil || afterOwner != owner {
			return nil, false, errors.Join(err, schemaddl.ErrConflict)
		}
		table, _, err := after.DesignTable()
		if err != nil {
			return nil, false, err
		}
		graph, err := schemaddl.NormalizeOpenGaussDesignGraph(*after, schemaddl.OpenGaussNamesForTable(table))
		if err != nil {
			return nil, false, err
		}
		count := 0
		for key, row := range graph.Records {
			if !strings.HasPrefix(key, "object:") || !strings.HasPrefix(graph.Objects[key], "@table:reltoastrelid") {
				continue
			}
			creator, err := gaussRecordUint(row["creator"])
			if err != nil || creator != owner || graph.Identities[key] == 0 {
				return nil, false, schemaddl.ErrConflict
			}
			if previous := prototypes[key]; previous != nil && schemaddl.Signature(previous["creator"]) != schemaddl.Signature(row["creator"]) {
				return nil, false, schemaddl.ErrConflict
			}
			prototypes[key] = gaussDesignCloneRecord(row)
			count++
		}
		if !removed && count == 0 || removed && count != 0 {
			return nil, false, schemaddl.ErrConflict
		}
	}
	return prototypes, removed, nil
}

func openGaussToastPrototypeScope(original, prototype *schemaddl.OpenGaussCatalog) bool {
	return prototype.NamespaceOID == original.NamespaceOID && prototype.Namespace == original.Namespace &&
		prototype.DatabaseOID == original.DatabaseOID && prototype.Database == original.Database &&
		prototype.RoleOID == original.RoleOID && prototype.NativeByteOrder == original.NativeByteOrder &&
		schemaddl.Signature(prototype.Classes) == schemaddl.Signature(original.Classes) &&
		prototype.CanCreate && prototype.CanManage && !prototype.Hooks
}
