package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type openGaussDesignOperator struct {
	oid, routine, left, right, result uint64
}

// Check every overload before parsing or constant folding. A native SQL
// wrapper is accepted only as a parameter reversal into an exact core native
// overload; the body is never sent to the server as an inference probe.
func openGaussCheckDesignOperators(ctx context.Context, c *sql.Conn, refs []schemaddl.PostgreSQLProbeOperator, checkType func(uint64) error) error {
	checkOperand := func(oid uint64) error {
		if oid == 0 {
			return nil // Unary operand slot.
		}
		var pseudo bool
		// Native anyset is a polymorphic operator signature despite typtype=s.
		// Limit this exception to its fixed core identity and I/O callbacks; an
		// actual SET declaration still goes through checkType and is rejected.
		err := c.QueryRowContext(ctx, `SELECT n.nspname='pg_catalog' AND (t.typtype='p' OR
 (t.oid=3272 AND t.typname='anyset' AND t.typtype='s' AND t.typelem=0 AND t.typarray=0
 AND t.typinput=3295 AND t.typoutput=3296 AND t.typreceive=0 AND t.typsend=0
 AND t.typmodin=0 AND t.typmodout=0 AND t.typanalyze=0))
 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE t.oid=$1::oid`, oid).Scan(&pseudo)
		if err != nil || oid >= 16384 {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
		if pseudo {
			return nil // Concrete declaration types remain in the cast closure.
		}
		return checkType(oid)
	}
	for _, ref := range refs {
		if ref.Schema != "" && ref.Schema != "pg_catalog" {
			return errOpenGaussDesignDeclaration
		}
		rows, err := c.QueryContext(ctx, `SELECT o.oid::bigint,o.oprcode::bigint,o.oprleft::bigint,o.oprright::bigint,o.oprresult::bigint
 FROM pg_operator o JOIN pg_namespace n ON n.oid=o.oprnamespace WHERE n.nspname='pg_catalog' AND o.oprname=$1`, ref.Name)
		if err != nil {
			return err
		}
		var operators []openGaussDesignOperator
		for rows.Next() {
			var op openGaussDesignOperator
			if err = rows.Scan(&op.oid, &op.routine, &op.left, &op.right, &op.result); err != nil {
				break
			}
			if len(operators) >= 512 {
				err = errOpenGaussDesignDeclaration
				break
			}
			operators = append(operators, op)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		if len(operators) == 0 {
			return errOpenGaussDesignDeclaration
		}
		for _, op := range operators {
			if err = openGaussCheckDesignOperatorRoutine(ctx, c, ref.Name, op, true); err != nil {
				return fmt.Errorf("native operator %s OID %d: %w", ref.Name, op.oid, err)
			}
			for _, oid := range []uint64{op.left, op.right, op.result} {
				if err = checkOperand(oid); err != nil {
					return fmt.Errorf("native operator %s operand/result type %d: %w", ref.Name, oid, err)
				}
			}
		}
	}
	return ctx.Err()
}

func openGaussCheckDesignOperatorRoutine(ctx context.Context, c *sql.Conn, name string, op openGaussDesignOperator, allowSQL bool) error {
	if op.oid == 0 || op.oid >= 16384 || op.routine == 0 || op.routine >= 16384 {
		return errOpenGaussDesignDeclaration
	}
	var namespace, volatility, source, args string
	var language, result uint64
	var aggregate, window, configured, returnsSet, securityDefiner bool
	err := c.QueryRowContext(ctx, `SELECT n.nspname,p.prolang::bigint,p.provolatile::text,p.prosrc,p.proargtypes::text,p.prorettype::bigint,
 p.proisagg,p.proiswindow,p.proconfig IS NOT NULL,p.proretset,p.prosecdef
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid=$1::oid`, op.routine).Scan(&namespace, &language, &volatility, &source, &args, &result, &aggregate, &window, &configured, &returnsSet, &securityDefiner)
	if err != nil || namespace != "pg_catalog" || aggregate || window || configured || returnsSet || securityDefiner || volatility != "i" && volatility != "s" || result != op.result {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	var expected []string
	for _, oid := range []uint64{op.left, op.right} {
		if oid != 0 {
			expected = append(expected, strconv.FormatUint(oid, 10))
		}
	}
	if strings.Join(strings.Fields(args), " ") != strings.Join(expected, " ") {
		return errOpenGaussDesignDeclaration
	}
	if language == 12 || language == 13 {
		return nil
	}
	// openGauss stores these core SQL bodies in prosrc, without PostgreSQL's
	// prosqlbody/prokind. Only the two observed parameter-only forms are valid.
	// No cast, literal, function call, schema lookup or additional statement is
	// authorized by this limited forwarder grammar.
	body := strings.Join(strings.Fields(source), " ")
	if !allowSQL || language != 14 || name != "+" || op.left == 0 || op.right == 0 || body != "select $2 + $1" && body != "select ($2 + $1)" {
		return errOpenGaussDesignDeclaration
	}
	var terminal openGaussDesignOperator
	err = c.QueryRowContext(ctx, `SELECT o.oid::bigint,o.oprcode::bigint,o.oprleft::bigint,o.oprright::bigint,o.oprresult::bigint
 FROM pg_operator o JOIN pg_namespace n ON n.oid=o.oprnamespace
 WHERE n.nspname='pg_catalog' AND o.oprname=$1 AND o.oprleft=$2::oid AND o.oprright=$3::oid AND o.oprresult=$4::oid`, name, op.right, op.left, op.result).Scan(&terminal.oid, &terminal.routine, &terminal.left, &terminal.right, &terminal.result)
	if err != nil || terminal.routine == op.routine {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	return openGaussCheckDesignOperatorRoutine(ctx, c, name, terminal, false)
}

// Preserve the explicit/assignment cast check over declaration/call types.
// Separately follow native implicit edges from every checked operand/result:
// user casts and non-core destinations must be rejected even when their type
// was absent from the written declaration. Merely reaching an unrelated core
// SQL cast does not authorize its body; actually bound SQL cast implementations
// still need independent native callback verification before executing values.
func openGaussCheckDesignCastClosure(ctx context.Context, c *sql.Conn, operands, declarations map[uint64]bool, checkRoutine func(uint64) error) error {
	var declared []string
	for oid := range declarations {
		declared = append(declared, strconv.FormatUint(oid, 10))
	}
	if len(declared) > 0 {
		rows, err := c.QueryContext(ctx, `SELECT ca.oid::bigint,ca.castfunc::bigint,ca.castmethod::text
 FROM pg_cast ca WHERE ca.castsource=ANY($1::oid[]) AND ca.casttarget=ANY($1::oid[])`, "{"+strings.Join(declared, ",")+"}")
		if err != nil {
			return err
		}
		type cast struct {
			oid, routine uint64
			method       string
		}
		var casts []cast
		for rows.Next() {
			var v cast
			if err = rows.Scan(&v.oid, &v.routine, &v.method); err != nil {
				break
			}
			if len(casts) >= 16384 {
				err = errOpenGaussDesignDeclaration
				break
			}
			casts = append(casts, v)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		for _, v := range casts {
			if v.oid == 0 || v.oid >= 16384 || v.method != "b" && v.method != "i" && v.method != "f" || v.method == "f" && v.routine == 0 || v.method != "f" && v.routine != 0 {
				return fmt.Errorf("native cast %d: %w", v.oid, errOpenGaussDesignDeclaration)
			}
			if err = checkRoutine(v.routine); err != nil {
				return fmt.Errorf("native cast %d routine %d: %w", v.oid, v.routine, err)
			}
		}
	}
	var inputs []string
	for oid := range operands {
		inputs = append(inputs, strconv.FormatUint(oid, 10))
	}
	if len(inputs) == 0 {
		return ctx.Err()
	}
	var count int
	var unsafe bool
	err := c.QueryRowContext(ctx, `WITH RECURSIVE reachable(oid) AS (
 SELECT unnest($1::oid[])
 UNION SELECT ca.casttarget FROM pg_cast ca JOIN reachable r ON r.oid=ca.castsource WHERE ca.castcontext='i'
 ) SELECT (SELECT count(*) FROM reachable), EXISTS(
 SELECT 1 FROM pg_cast ca JOIN reachable r ON r.oid=ca.castsource
 LEFT JOIN pg_type t ON t.oid=ca.casttarget LEFT JOIN pg_namespace tn ON tn.oid=t.typnamespace
 LEFT JOIN pg_proc p ON p.oid=ca.castfunc LEFT JOIN pg_namespace pn ON pn.oid=p.pronamespace
 WHERE ca.castcontext='i' AND (ca.oid>=16384 OR ca.casttarget>=16384 OR t.oid IS NULL OR tn.nspname<>'pg_catalog'
 OR ca.castmethod NOT IN('b','i','f') OR ca.castmethod<>'f' AND ca.castfunc<>0
 OR ca.castmethod='f' AND (p.oid IS NULL OR p.oid>=16384 OR pn.nspname<>'pg_catalog' OR p.prolang NOT IN(12,13,14)
 OR p.provolatile NOT IN('i','s') OR p.proisagg OR p.proiswindow OR p.proretset OR p.prosecdef OR p.proconfig IS NOT NULL)))`, "{"+strings.Join(inputs, ",")+"}").Scan(&count, &unsafe)
	if err != nil || unsafe || count > 4096 {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	return ctx.Err()
}
