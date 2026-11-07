package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

const openGaussAccessMethodCatalogSQL = `SELECT a.oid::bigint,row_to_json(a)::text,
 json_build_array(a.aminsert::oid::bigint,a.ambeginscan::oid::bigint,
 a.amgettuple::oid::bigint,a.amgetbitmap::oid::bigint,a.amrescan::oid::bigint,a.amendscan::oid::bigint,a.ammarkpos::oid::bigint,
 a.amrestrpos::oid::bigint,a.ammerge::oid::bigint,a.ambuild::oid::bigint,a.ambuildempty::oid::bigint,a.ambulkdelete::oid::bigint,
 a.amvacuumcleanup::oid::bigint,a.amcanreturn::oid::bigint,a.amcostestimate::oid::bigint,a.amoptions::oid::bigint,a.amhandler::oid::bigint)::text
 FROM pg_am a WHERE a.amname=$1`

// Validate the actual native dispatch table and its fixed implementation
// signatures before an independent index declaration can execute callbacks.
// This check does not acquire catalog locks or authorize concurrent replacements.
func openGaussCheckNativeAccessMethod(ctx context.Context, c *sql.Conn, name string, oid uint64, definition string, callbacks []uint64) error {
	spec, known := openGaussNativeAccessMethods[name]
	if !known || oid != spec.OID || len(callbacks) != len(spec.Routines) {
		return errOpenGaussDesignDeclaration
	}
	var actual, expected map[string]any
	if json.Unmarshal([]byte(definition), &actual) != nil || json.Unmarshal([]byte(spec.Definition), &expected) != nil || !reflect.DeepEqual(actual, expected) {
		return errOpenGaussDesignDeclaration
	}
	ids := make([]string, 0, len(callbacks))
	seen := map[uint64]bool{}
	for i, id := range callbacks {
		if id != spec.Routines[i] {
			return errOpenGaussDesignDeclaration
		}
		if id != 0 && !seen[id] {
			ids = append(ids, strconv.FormatUint(id, 10))
			seen[id] = true
		}
	}
	rows, err := c.QueryContext(ctx, `SELECT p.oid::bigint,n.nspname,p.proname,p.prosrc,p.proargtypes::text,
 p.prorettype::bigint,p.prolang::bigint,p.provolatile::text,p.proisstrict,p.pronargs,
 p.proisagg OR p.proiswindow OR p.proretset OR p.prosecdef OR p.proconfig IS NOT NULL
 OR p.probin IS NOT NULL OR p.provariadic<>0 OR p.pronargdefaults<>0
 OR p.proallargtypes IS NOT NULL OR p.proargmodes IS NOT NULL OR p.proargnames IS NOT NULL OR p.proargdefaults IS NOT NULL
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid=ANY($1::oid[])`, "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, result, language uint64
		var namespace, routine, source, arguments, volatility string
		var strict, unsafe bool
		var arity int
		if err = rows.Scan(&id, &namespace, &routine, &source, &arguments, &result, &language, &volatility, &strict, &arity, &unsafe); err != nil {
			return err
		}
		native, known := openGaussNativeAccessMethodRoutines[id]
		if !known || !seen[id] || namespace != "pg_catalog" || routine != native.Name || source != native.Name || arguments != native.Arguments || result != native.Result || language != 12 || volatility != native.Volatility || !strict || unsafe || arity != len(strings.Fields(native.Arguments)) {
			return fmt.Errorf("native %s callback %d has changed implementation: %w", name, id, errOpenGaussDesignDeclaration)
		}
		seen[id] = false
		count++
	}
	if err = rows.Err(); err != nil || count != len(ids) {
		return errors.Join(err, errOpenGaussDesignDeclaration)
	}
	return ctx.Err()
}
