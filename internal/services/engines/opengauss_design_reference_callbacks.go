package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// Inspect the native FK's actually selected key types, equality operators and
// parent index classes before validation can read rows. No parent values,
// default expressions or sequence states are evaluated by these checks.
func openGaussCheckDesignReferenceCallbacks(ctx context.Context, c *sql.Conn, child, parent *schemaddl.OpenGaussCatalog) (err error) {
	phase := "scope"
	defer func() {
		if err != nil {
			err = fmt.Errorf("native reference callbacks %s: %w", phase, err)
		}
	}()
	if child == nil || parent == nil || child.RoleOID != parent.RoleOID || child.DatabaseOID != parent.DatabaseOID || child.Hooks || parent.Hooks {
		return schemaddl.ErrConflict
	}
	for _, entry := range parent.Entries {
		if entry.Kind == "policy" {
			return errors.New("native reference row policies require independent callback verification")
		}
	}
	// These internal trigger routines are volatile by design. Their exact
	// native signatures and implementation flags must still be verified.
	for id, name := range map[uint64]string{
		1644: "RI_FKey_check_ins", 1645: "RI_FKey_check_upd", 1646: "RI_FKey_cascade_del", 1647: "RI_FKey_cascade_upd",
		1648: "RI_FKey_restrict_del", 1649: "RI_FKey_restrict_upd", 1650: "RI_FKey_setnull_del", 1651: "RI_FKey_setnull_upd",
		1652: "RI_FKey_setdefault_del", 1653: "RI_FKey_setdefault_upd", 1654: "RI_FKey_noaction_del", 1655: "RI_FKey_noaction_upd",
	} {
		phase = "routine " + name
		var trusted bool
		err := c.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE p.oid=$1::oid AND p.proname=$2 AND p.prosrc=$2 AND n.nspname='pg_catalog' AND p.prolang IN(12,13)
 AND p.provolatile='v' AND p.prorettype=2279 AND p.proargtypes::text='' AND NOT p.proisagg AND NOT p.proiswindow
 AND NOT p.proretset AND NOT p.prosecdef AND p.proconfig IS NULL)`, id, name).Scan(&trusted)
		if err != nil || !trusted {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
	}
	operators := map[uint64]bool{}
	classes := map[uint64]bool{}
	types := schemaddl.Table{Name: "__nyadb_reference_types", OpenGauss: &schemaddl.OpenGaussTableProperties{Persistence: "p"}}
	seenTypes := map[uint64]bool{}
	addType := func(relation uint64, number int) error {
		phase = fmt.Sprintf("key type %d/%d", relation, number)
		var id uint64
		var native bool
		var declaration string
		err := c.QueryRowContext(ctx, `SELECT a.atttypid::bigint,n.nspname='pg_catalog' AND t.typtype='b' AND t.oid<16384,
 CASE WHEN n.nspname='pg_catalog' AND t.typtype='b' AND t.oid<16384 THEN format_type(a.atttypid,a.atttypmod) ELSE '' END
 FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid JOIN pg_namespace n ON n.oid=t.typnamespace
 WHERE a.attrelid=$1::oid AND a.attnum=$2 AND NOT a.attisdropped`, relation, number).Scan(&id, &native, &declaration)
		if err != nil || !native {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
		if !seenTypes[id] {
			name := fmt.Sprintf("key_%d", len(types.Clauses))
			types.Clauses = append(types.Clauses, schemaddl.Clause{ID: name, Kind: "column", Name: name, SQL: declaration})
			seenTypes[id] = true
		}
		return nil
	}
	count := 0
	for _, entry := range child.Entries {
		if entry.Kind != "constraint" {
			continue
		}
		var fk struct {
			Kind      string   `json:"contype"`
			Child     uint64   `json:"conrelid"`
			Parent    uint64   `json:"confrelid"`
			Index     uint64   `json:"conindid"`
			Key       []int    `json:"conkey"`
			ParentKey []int    `json:"confkey"`
			ParentEq  []uint64 `json:"conppeqop"`
			CrossEq   []uint64 `json:"conpfeqop"`
			ChildEq   []uint64 `json:"conffeqop"`
		}
		if json.Unmarshal(entry.Data, &fk) != nil {
			return schemaddl.ErrConflict
		}
		if fk.Kind != "f" || fk.Parent != parent.RelationOID {
			continue
		}
		if fk.Child != child.RelationOID || fk.Index == 0 || len(fk.Key) == 0 || len(fk.ParentKey) != len(fk.Key) || len(fk.ParentEq) != len(fk.Key) || len(fk.CrossEq) != len(fk.Key) || len(fk.ChildEq) != len(fk.Key) {
			return schemaddl.ErrConflict
		}
		count++
		for i, number := range fk.Key {
			if number <= 0 || fk.ParentKey[i] <= 0 {
				return schemaddl.ErrConflict
			}
			if err := addType(fk.Child, number); err != nil {
				return err
			}
			if err := addType(fk.Parent, fk.ParentKey[i]); err != nil {
				return err
			}
			for _, op := range []uint64{fk.ParentEq[i], fk.CrossEq[i], fk.ChildEq[i]} {
				if op == 0 || op >= 16384 {
					return errOpenGaussDesignDeclaration
				}
				operators[op] = true
			}
		}
		found := false
		phase = fmt.Sprintf("parent index %d", fk.Index)
		for _, index := range parent.Entries {
			if index.Kind != "index" || index.OID != fk.Index {
				continue
			}
			var raw struct {
				Parent   uint64   `json:"indrelid"`
				Unique   bool     `json:"indisunique"`
				Valid    bool     `json:"indisvalid"`
				Ready    bool     `json:"indisready"`
				Usable   bool     `json:"indisusable"`
				Keys     []int    `json:"indkey"`
				Class    []uint64 `json:"indclass"`
				KeyCount int      `json:"indnkeyatts"`
				Pred     string   `json:"indpred"`
				Expr     string   `json:"indexprs"`
			}
			if json.Unmarshal(index.Data, &raw) != nil || raw.Parent != fk.Parent || !raw.Unique || !raw.Valid || !raw.Ready || !raw.Usable || raw.Pred != "" || raw.Expr != "" {
				return errOpenGaussDesignDeclaration
			}
			if raw.KeyCount <= 0 || raw.KeyCount > len(raw.Keys) || raw.KeyCount > len(raw.Class) {
				return schemaddl.ErrConflict
			}
			for _, key := range fk.ParentKey {
				selected := false
				for i, number := range raw.Keys[:raw.KeyCount] {
					if number != key {
						continue
					}
					oid := raw.Class[i]
					if oid == 0 || oid >= 16384 || selected {
						return errOpenGaussDesignDeclaration
					}
					classes[oid], selected = true, true
				}
				if !selected {
					return schemaddl.ErrConflict
				}
			}
			found = true
		}
		if !found {
			return schemaddl.ErrConflict
		}
		methodFound := false
		for _, entry := range parent.Entries {
			if entry.Kind != "relation" || entry.OID != fk.Index {
				continue
			}
			var relation struct {
				Method uint64 `json:"relam"`
			}
			if json.Unmarshal(entry.Data, &relation) != nil || relation.Method != 403 {
				return errOpenGaussDesignDeclaration
			}
			methodFound = true
		}
		if !methodFound {
			return fmt.Errorf("native parent index relation %d is absent from captured catalog: %w", fk.Index, schemaddl.ErrConflict)
		}
	}
	if count == 0 {
		return schemaddl.ErrConflict
	}
	phase = "key declarations"
	if err := openGaussCheckDesignDeclaration(ctx, c, types, child.Namespace); err != nil {
		return err
	}
	for id := range operators {
		phase = fmt.Sprintf("equality operator %d", id)
		var op openGaussDesignOperator
		var trusted bool
		err := c.QueryRowContext(ctx, `SELECT o.oid::bigint,o.oprcode::bigint,o.oprleft::bigint,o.oprright::bigint,o.oprresult::bigint,
 n.nspname='pg_catalog' AND o.oprname='=' AND o.oprresult=16 FROM pg_operator o JOIN pg_namespace n ON n.oid=o.oprnamespace WHERE o.oid=$1::oid`, id).Scan(&op.oid, &op.routine, &op.left, &op.right, &op.result, &trusted)
		if err != nil || !trusted {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
		if err = openGaussCheckDesignOperatorRoutine(ctx, c, "=", op, false); err != nil {
			return err
		}
	}
	var classIDs []string
	for oid := range classes {
		classIDs = append(classIDs, strconv.FormatUint(oid, 10))
	}
	phase = "selected operator classes"
	return openGaussCheckDesignOperatorClasses(ctx, c, "{"+strings.Join(classIDs, ",")+"}")
}
