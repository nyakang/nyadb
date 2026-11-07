package engines

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func gaussDesignCloneRecord(v map[string]any) map[string]any {
	x := map[string]any{}
	for k, value := range v {
		x[k] = value
	}
	return x
}

func openGaussShapeTable(table schemaddl.Table, p *schemaddl.OpenGaussCatalog) (schemaddl.Table, []schemaddl.OpenGaussCopySequence, error) {
	d, err := p.CopyDefinition()
	if err != nil {
		return table, nil, err
	}
	t := table.Clone()
	for i, c := range t.Clauses {
		if c.Kind != "column" {
			continue
		}
		for _, raw := range d.Columns {
			if raw.Name == c.Name {
				if t.Clauses[i].OpenGauss == nil {
					t.Clauses[i].OpenGauss = &schemaddl.OpenGaussColumnProperties{}
				}
				t.Clauses[i].OpenGauss.Number = raw.Number
			}
		}
	}
	return t, d.Sequences, nil
}

// Complete immutable records are kept for retained objects. Intentional field
// and object changes acquire native values only from the independent CREATE.
// Unknown properties are never copied from the actual result to make it pass.
func openGaussVerifyDesignCatalog(original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline, actual contract.SchemaDesignSnapshot) error {
	return openGaussVerifyDesignCatalogWithIncoming(original, desired, baseline, actual, nil)
}

func openGaussVerifyDesignCatalogWithIncoming(original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline, actual contract.SchemaDesignSnapshot, incomingActual map[uint64]*schemaddl.OpenGaussCatalog) error {
	wantedTarget := original.Target
	wantedTarget.Object.Name = desired.Name
	if actual.Schema.OpenGauss == nil || !actual.Schema.Exists || actual.Target != wantedTarget {
		return schemaddl.ErrConflict
	}
	old := original.Schema.OpenGauss
	current := actual.Schema.OpenGauss
	revision, err := current.Fingerprint()
	if err != nil || revision != actual.Schema.Revision {
		return errors.Join(err, schemaddl.ErrConflict)
	}
	if old == nil || baseline.Catalog == nil || schemaddl.Signature(old.Classes) != schemaddl.Signature(current.Classes) || old.NativeByteOrder != current.NativeByteOrder {
		return schemaddl.ErrConflict
	}
	if current.Database != old.Database || current.Namespace != old.Namespace || current.Name != desired.Name || current.DatabaseOID != old.DatabaseOID || current.NamespaceOID != old.NamespaceOID || current.RoleOID != old.RoleOID || current.CanCreate != old.CanCreate || !current.CanManage || old.Hooks || current.Hooks {
		return schemaddl.ErrConflict
	}
	currentDef, err := current.CopyDefinition()
	if err != nil {
		return err
	}
	wanted := baseline.Table.Clone()
	if original.Schema.Exists {
		if current.RelationOID != old.RelationOID || currentDef.Owner != original.Schema.Table.OpenGauss.Owner {
			return errors.New("native design changed original table identity or owner")
		}
	} else {
		if currentDef.Owner != currentDef.Creator {
			return errors.New("native new table owner differs from captured creator")
		}
		wanted.OpenGauss.Owner = currentDef.Creator
	}
	wanted, probeSequences, err := openGaussShapeTable(wanted, baseline.Catalog)
	if err != nil {
		return err
	}
	if original.Schema.Exists {
		prior, err := old.CopyDefinition()
		if err != nil {
			return err
		}
		for _, seq := range prior.Sequences {
			id := ""
			for _, col := range original.Schema.Table.Columns() {
				if col.OpenGauss != nil && col.OpenGauss.Number == seq.Column {
					id = col.ID
				}
			}
			for _, col := range wanted.Columns() {
				if col.ID == id && col.OpenGauss != nil {
					seq.Column = col.OpenGauss.Number
					probeSequences = append(probeSequences, seq)
				}
			}
		}
	}
	// Private normalized constraints/definitions have already been rebound to
	// desired names. Rejoin new serial ownership by the declared field name.
	for i := range probeSequences {
		for _, raw := range baseline.Table.Columns() {
			if raw.OpenGauss != nil && raw.OpenGauss.Number == probeSequences[i].Column {
				probeSequences[i].Column = raw.OpenGauss.Number
			}
		}
	}
	want, err := schemaddl.OpenGaussDesignDeclarationShape(wanted, schemaddl.OpenGaussNamesForTable(wanted), probeSequences)
	if err != nil {
		return err
	}
	got, err := schemaddl.OpenGaussDesignDeclarationShape(actual.Schema.Table, schemaddl.OpenGaussNamesForTable(desired), currentDef.Sequences)
	if err != nil || want != got {
		return errors.Join(err, errors.New("native normalized declaration differs from requested design"))
	}
	actualNames := schemaddl.OpenGaussNamesForTable(desired)
	actualNames.ReferenceRelations = baseline.Names.ReferenceRelations
	actualNames, err = openGaussDesignIncomingGraphNames(actualNames, original, desired, baseline, current, incomingActual, "actual")
	if err != nil {
		return err
	}
	actualGraph, err := schemaddl.NormalizeOpenGaussDesignGraph(*current, actualNames)
	if err != nil {
		return fmt.Errorf("actual native graph: %w", err)
	}
	probeNames, err := openGaussDesignIncomingGraphNames(baseline.Names, original, desired, baseline, baseline.Catalog, nil, "probe")
	if err != nil {
		return err
	}
	probeGraph, err := schemaddl.NormalizeOpenGaussDesignGraph(*baseline.Catalog, probeNames)
	if err != nil {
		return fmt.Errorf("probe native graph: %w", err)
	}
	var beforeGraph schemaddl.OpenGaussDesignGraph
	if original.Schema.Exists {
		beforeNames := schemaddl.OpenGaussNamesForTable(original.Schema.Table)
		beforeNames.ReferenceRelations = baseline.Names.ReferenceRelations
		beforeNames, err = openGaussDesignIncomingGraphNames(beforeNames, original, desired, baseline, old, nil, "original")
		if err != nil {
			return err
		}
		beforeGraph, err = schemaddl.NormalizeOpenGaussDesignGraph(*old, beforeNames)
		if err != nil {
			return fmt.Errorf("original native graph: %w", err)
		}
	} else {
		beforeGraph.Records = map[string]map[string]any{}
	}
	if err = gaussDesignAlterDeltaPrototype(&probeGraph, original, desired); err != nil {
		return err
	}
	expected := map[string]map[string]any{}
	for key, row := range beforeGraph.Records {
		expected[key] = gaussDesignCloneRecord(row)
	}
	oldColumns := map[string]schemaddl.Clause{}
	for _, c := range original.Schema.Table.Columns() {
		oldColumns[c.ID] = c
	}
	newColumns := map[string]schemaddl.Clause{}
	for _, c := range desired.Columns() {
		newColumns[c.ID] = c
	}
	for id := range oldColumns {
		if _, ok := newColumns[id]; !ok {
			delete(expected, "column:@table:@column:"+id)
			delete(expected, "column:@table:reldeltarelid:@column:"+id)
			delete(expected, "default:@default:@column:"+id)
		}
	}
	for id, c := range newColumns {
		oldColumn, retained := oldColumns[id]
		for _, owner := range []string{"@table", "@table:reldeltarelid"} {
			key := "column:" + owner + ":@column:" + id
			prototype := probeGraph.Records[key]
			if prototype == nil {
				if owner == "@table" || expected[key] != nil {
					return schemaddl.ErrConflict
				}
				continue
			}
			if !retained {
				expected[key] = gaussDesignCloneRecord(prototype)
				continue
			}
			if expected[key] == nil {
				return schemaddl.ErrConflict
			}
			a, e := schemaddl.ParseOpenGaussField(oldColumn.SQL)
			if e != nil {
				return e
			}
			b, e := schemaddl.ParseOpenGaussField(c.SQL)
			if e != nil {
				return e
			}
			if a.Type != b.Type {
				for _, field := range []string{"atttypid", "atttypmod", "attlen", "attbyval", "attalign", "attstorage", "attndims"} {
					expected[key][field] = prototype[field]
				}
			}
			if a.Collation != b.Collation {
				expected[key]["attcollation"] = prototype["attcollation"]
			}
			if a.NotNull != b.NotNull {
				expected[key]["attnotnull"] = prototype["attnotnull"]
			}
			if a.HasDefault != b.HasDefault || a.Default != b.Default {
				expected[key]["atthasdef"] = prototype["atthasdef"]
			}
		}
		defaultKey := "default:@default:@column:" + id
		oldField, _ := schemaddl.ParseOpenGaussField(oldColumn.SQL)
		nextField, _ := schemaddl.ParseOpenGaussField(c.SQL)
		if !retained || oldField.Type != nextField.Type || oldField.Default != nextField.Default || oldField.HasDefault != nextField.HasDefault {
			if row := probeGraph.Records[defaultKey]; row != nil {
				expected[defaultKey] = gaussDesignCloneRecord(row)
			} else {
				delete(expected, defaultKey)
			}
		}
	}
	typeChanged := false
	for id, next := range newColumns {
		if previous, ok := oldColumns[id]; ok {
			a, _ := schemaddl.ParseOpenGaussField(previous.SQL)
			b, _ := schemaddl.ParseOpenGaussField(next.SQL)
			typeChanged = typeChanged || a.Type != b.Type
		}
	}
	// Direct declaration objects intentionally removed/recreated by the plan.
	changed := map[string]bool{}
	// Native ALTER may first create TOAST storage when a variable-length
	// field is added. Its topology comes from the independent declaration;
	// existing storage is retained even if CREATE no longer requires it.
	for _, owner := range []string{"@table", "@table:reldeltarelid"} {
		main, prototype := expected["relation:"+owner], probeGraph.Records["relation:"+owner]
		if main == nil || prototype == nil {
			continue
		}
		if main["parttype"] != "n" {
			continue // partition storage belongs to captured pg_partition records
		}
		for _, field := range []string{"reltoastrelid", "reltoastidxid"} {
			// The native type rewrite swaps TOAST by links. If independent
			// CREATE needs no TOAST for the resulting descriptor, the old
			// internal tree is removed. A plain DROP COLUMN does not rewrite
			// storage and therefore does not receive this removal permission.
			if typeChanged && fmt.Sprint(prototype[field]) == "0" {
				if root, hadStorage := main[field].(string); hadStorage {
					main[field] = prototype[field]
					for _, id := range beforeGraph.Objects {
						if strings.Contains(id, root) {
							changed[id] = true
						}
					}
				}
			}
			root, fresh := prototype[field].(string)
			if !fresh || fmt.Sprint(main[field]) != "0" {
				continue
			}
			main[field] = root
			for _, id := range probeGraph.Objects {
				if strings.Contains(id, root) {
					changed[id] = true
				}
			}
		}
	}
	for _, c := range original.Schema.Table.Clauses {
		if c.Kind != "constraint" {
			continue
		}
		found := false
		for _, next := range desired.Clauses {
			if next.ID == c.ID && next.SQL == c.SQL && next.Name == c.Name {
				found = true
			}
		}
		if !found {
			changed["@constraint:"+c.ID] = true
			changed["@constraint-index:"+c.ID] = true
		}
	}
	for _, c := range desired.Clauses {
		if c.Kind != "constraint" {
			continue
		}
		found := false
		for _, previous := range original.Schema.Table.Clauses {
			if previous.ID == c.ID && previous.SQL == c.SQL && previous.Name == c.Name {
				found = true
			}
		}
		if !found {
			changed["@constraint:"+c.ID] = true
			changed["@constraint-index:"+c.ID] = true
		}
	}
	for _, d := range original.Schema.Table.Definitions {
		found := false
		for _, next := range desired.Definitions {
			if d.Kind == next.Kind && d.Name == next.Name && d.SQL == next.SQL {
				found = true
			}
		}
		if !found {
			changed["@"+d.Kind+":"+d.Name] = true
		}
	}
	for _, d := range desired.Definitions {
		found := false
		for _, previous := range original.Schema.Table.Definitions {
			if d.Kind == previous.Kind && d.Name == previous.Name && d.SQL == previous.SQL {
				found = true
			}
		}
		if !found {
			changed["@"+d.Kind+":"+d.Name] = true
		}
	}
	for key := range expected {
		for id := range changed {
			if strings.Contains(key, ":"+id) {
				delete(expected, key)
			}
		}
	}
	for key, row := range probeGraph.Records {
		fresh := !original.Schema.Exists
		for id := range changed {
			fresh = fresh || strings.Contains(key, ":"+id)
		}
		if fresh {
			expected[key] = gaussDesignCloneRecord(row)
		}
	}
	// Derived counts describe physical attribute slots and actual CHECKs.
	if original.Schema.Exists {
		for _, owner := range []string{"@table", "@table:reldeltarelid"} {
			main := expected["relation:"+owner]
			if main == nil {
				continue
			}
			var added uint64
			for id := range newColumns {
				if _, ok := oldColumns[id]; !ok {
					added++
				}
			}
			n, err := gaussRecordUint(main["relnatts"])
			if err != nil {
				return err
			}
			main["relnatts"] = n + added
			if prototype := probeGraph.Records["relation:"+owner]; prototype != nil {
				main["relchecks"] = prototype["relchecks"]
				// Like relhasindex, native relhaspkey is a positive cache flag.
				// DROP does not immediately clear it; real constraints and index
				// objects are independently verified above and in aggregates.
				main["relhaspkey"] = main["relhaspkey"] == true || prototype["relhaspkey"] == true
			}
		}
	}
	// Comments/grants/dependency sets must be reconstructed from declared
	// changes, rather than accepted wholesale from the after-image.
	rebound, err := gaussDesignTypeRebindings(expected, beforeGraph, probeGraph, original, desired)
	if err != nil {
		return err
	}
	if err = gaussDesignExpectedAggregates(expected, beforeGraph, probeGraph, actualGraph, original, desired, changed, rebound); err != nil {
		return err
	}
	// Native ExecRewriteRowTable calls finish_heap_swap with toast-by-links.
	// Adding a stored generated field necessarily rewrites an ordinary heap;
	// its internal TOAST tree can acquire new physical identities. Preserve
	// every record/property and all original user object identities regardless.
	rewriteGenerated := false
	if main := beforeGraph.Records["relation:@table"]; main != nil && main["parttype"] == "n" {
		columnStore := false
		for _, option := range original.Schema.Table.OpenGauss.Parameters {
			columnStore = columnStore || strings.EqualFold(strings.ReplaceAll(option, " ", ""), "orientation=column")
		}
		if !columnStore {
			for id, col := range newColumns {
				if _, existed := oldColumns[id]; existed {
					continue
				}
				f, e := schemaddl.ParseOpenGaussField(col.SQL)
				if e != nil {
					return e
				}
				rewriteGenerated = rewriteGenerated || f.Generation != ""
			}
		}
	}
	for key, want := range expected {
		got := actualGraph.Records[key]
		if got == nil {
			return fmt.Errorf("native preserved catalog record disappeared: %s", key)
		}
		retained := original.Schema.Exists && beforeGraph.Records[key] != nil && !gaussChangedGraphKey(key, changed)
		if rebound[beforeGraph.Objects[key]] && beforeGraph.Identities[key] != actualGraph.Identities[key] {
			retained = false
		}
		if rewriteGenerated && beforeGraph.Identities[key] != actualGraph.Identities[key] {
			object := beforeGraph.Objects[key]
			if strings.HasPrefix(object, "@table:reltoastrelid") || strings.HasPrefix(object, "@table:reltoastidxid") || strings.HasPrefix(object, "@physical-index:@table:reltoast") {
				retained = false
			}
		}
		if strings.HasPrefix(key, "default:") {
			for id, previous := range oldColumns {
				next, ok := newColumns[id]
				if !ok || !strings.HasSuffix(key, "@column:"+id) {
					continue
				}
				a, _ := schemaddl.ParseOpenGaussField(previous.SQL)
				b, _ := schemaddl.ParseOpenGaussField(next.SQL)
				if a.Type != b.Type || a.Default != b.Default || a.HasDefault != b.HasDefault {
					retained = false
				}
			}
		}
		if (strings.Contains(key, ":@physical-index:") || strings.Contains(key, "@table:rel")) && beforeGraph.Identities[key] != actualGraph.Identities[key] {
			for id, next := range newColumns {
				if previous, ok := oldColumns[id]; ok {
					a, _ := schemaddl.ParseOpenGaussField(previous.SQL)
					b, _ := schemaddl.ParseOpenGaussField(next.SQL)
					if a.Type != b.Type {
						retained = false
					}
				}
			}
		}
		if retained && beforeGraph.Identities[key] != actualGraph.Identities[key] {
			return fmt.Errorf("native retained catalog identity changed: %s", key)
		}
		if !retained && beforeGraph.Identities[key] != 0 && beforeGraph.Identities[key] != actualGraph.Identities[key] && strings.HasPrefix(key, "object:") {
			prototype := probeGraph.Records[key]
			if prototype == nil || prototype["creator"] == nil {
				return schemaddl.ErrConflict
			}
			// A proven native user/storage index reconstruction records this authenticated actor
			// as creator. Retained objects and every other property keep
			// their captured original values.
			want = gaussDesignCloneRecord(want)
			want["creator"] = prototype["creator"]
		}
		want = gaussDesignComparableRecord(key, want, retained)
		got = gaussDesignComparableRecord(key, got, retained)
		if schemaddl.Signature(want) != schemaddl.Signature(got) {
			var fields []string
			for field, value := range want {
				if schemaddl.Signature(value) != schemaddl.Signature(got[field]) {
					fields = append(fields, field)
				}
			}
			for field := range got {
				if _, ok := want[field]; !ok {
					fields = append(fields, field)
				}
			}
			return fmt.Errorf("native catalog differs from independent expected record: %s (%v)", key, fields)
		}
	}
	for key := range actualGraph.Records {
		if expected[key] == nil {
			return fmt.Errorf("unexpected native catalog record after design: %s", key)
		}
	}
	return nil
}

func gaussRecordUint(v any) (uint64, error) {
	switch n := v.(type) {
	case uint64:
		return n, nil
	case int:
		return uint64(n), nil
	default:
		var value uint64
		_, err := fmt.Sscan(fmt.Sprint(n), &value)
		return value, err
	}
}
func gaussChangedGraphKey(key string, changed map[string]bool) bool {
	for id := range changed {
		if strings.Contains(key, ":"+id) {
			return true
		}
	}
	return false
}
func gaussDesignComparableRecord(key string, row map[string]any, retained bool) map[string]any {
	x := gaussDesignCloneRecord(row)
	if strings.HasPrefix(key, "index:") {
		delete(x, "indcheckxmin")
	}
	if strings.HasPrefix(key, "object:") && !retained {
		delete(x, "ctime")
		delete(x, "createcsn")
	}
	return x
}
