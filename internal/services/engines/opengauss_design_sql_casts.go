package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type openGaussNativeSQLRoutine struct {
	name, args, volatility, body string
	result                       uint64
	callees                      []uint64
}

type openGaussNativeSQLCast struct {
	source, target, routine uint64
	context, method         string
}

// These are the native 6.0 SQL cast identities, including the non-cast
// to_number(text) forwarder they call. Bodies are checked structurally before
// CREATE can fold a constant. They are not inferred by executing SQL.
var openGaussNativeSQLCasts = map[uint64]openGaussNativeSQLCast{
	11841: {702, 1083, 1364, "a", "f"}, 11880: {718, 604, 1544, "e", "f"},
	12489: {1186, 25, 12488, "i", "f"}, 12491: {1083, 25, 12490, "i", "f"}, 12493: {1266, 25, 12492, "i", "f"},
	12495: {703, 25, 12494, "i", "f"}, 12497: {702, 25, 12496, "i", "f"}, 12527: {1186, 1043, 12526, "i", "f"},
	12530: {1042, 1186, 12528, "i", "f"}, 12531: {1043, 1186, 12529, "i", "f"},
	12536: {1114, 9003, 12535, "i", "f"}, 12538: {9003, 1114, 12537, "i", "f"}, 12540: {9003, 25, 12539, "i", "f"},
	12542: {9003, 1043, 12541, "i", "f"}, 12545: {1043, 9003, 12543, "i", "f"}, 12546: {1042, 9003, 12544, "i", "f"},
	12548: {702, 9003, 12547, "i", "f"}, 12550: {9003, 702, 12549, "i", "f"}, 12552: {9003, 1083, 12551, "i", "f"},
	12554: {9003, 1184, 12553, "i", "f"}, 12556: {1184, 9003, 12555, "i", "f"},
	12567: {3969, 1700, 12566, "i", "f"}, 12569: {3969, 23, 12568, "i", "f"}, 12571: {1114, 3969, 12570, "i", "f"},
	12573: {1186, 3969, 12572, "i", "f"}, 12580: {21, 3969, 12575, "i", "f"}, 12581: {23, 3969, 12576, "i", "f"},
	12582: {20, 3969, 12577, "i", "f"}, 12583: {1700, 3969, 12574, "i", "f"}, 12584: {700, 3969, 12578, "i", "f"},
	12585: {701, 3969, 12579, "i", "f"}, 12587: {3969, 1114, 12586, "i", "f"},
}

var openGaussNativeSQLRoutines = map[uint64]openGaussNativeSQLRoutine{
	1364:  {"time", "702", "s", "select cast(cast($1 as timestamp without time zone) as pg_catalog.time)", 1083, []uint64{2023, 1316}},
	1544:  {"polygon", "718", "i", "select pg_catalog.polygon(12, $1)", 604, []uint64{1475}},
	12488: {"to_text", "1186", "i", "select CAST(pg_catalog.interval_out($1) AS TEXT)", 25, []uint64{1161}},
	12490: {"time_text", "1083", "i", "select CAST(pg_catalog.time_out($1) AS text)", 25, []uint64{1144}},
	12492: {"timetz_text", "1266", "i", "select CAST(pg_catalog.timetz_out($1) AS text)", 25, []uint64{1351}},
	12494: {"reltime_text", "703", "i", "select CAST(pg_catalog.reltimeout($1) AS text)", 25, []uint64{243}},
	12496: {"abstime_text", "702", "i", "select CAST(pg_catalog.abstimeout($1) AS text)", 25, []uint64{241}},
	12526: {"to_varchar2", "1186", "i", "select CAST(pg_catalog.interval_out($1) AS VARCHAR2)", 1043, []uint64{1161}},
	12528: {"to_interval", "1042 23", "i", "select pg_catalog.interval_in(pg_catalog.bpcharout($1), 0::Oid, $2)", 1186, []uint64{1160, 1045}},
	12529: {"to_interval", "1043 23", "i", "select pg_catalog.interval_in(pg_catalog.varcharout($1), 0::Oid, $2)", 1186, []uint64{1160, 1047}},
	12535: {"timestamp_to_smalldatetime", "1114", "i", "select pg_catalog.smalldatetime_in(pg_catalog.timestamp_out($1), 0::Oid, -1)", 9003, []uint64{9004, 1313}},
	12537: {"smalldatetime_to_timestamp", "9003", "i", "select pg_catalog.timestamp_in(pg_catalog.smalldatetime_out($1), 0::Oid, -1)", 1114, []uint64{1312, 9006}},
	12539: {"to_text", "9003", "i", "select CAST(pg_catalog.smalldatetime_out($1) AS VARCHAR2)", 25, []uint64{9006}},
	12541: {"smalldatetime_to_varchar2", "9003", "i", "select CAST(pg_catalog.smalldatetime_out($1) AS VARCHAR2)", 1043, []uint64{9006}},
	12543: {"varchar2_to_smlldatetime", "1043", "i", "select pg_catalog.smalldatetime_in(pg_catalog.varcharout($1), 0::Oid, -1)", 9003, []uint64{9004, 1047}},
	12544: {"bpchar_to_smalldatetime", "1042", "i", "select pg_catalog.smalldatetime_in(pg_catalog.bpcharout($1), 0::Oid, -1)", 9003, []uint64{9004, 1045}},
	12547: {"abstime_to_smalldatetime", "702", "i", "select pg_catalog.smalldatetime_in(pg_catalog.timestamp_out($1), 0::Oid, -1)", 9003, []uint64{9004, 1313, 2023}},
	12549: {"smalldatetime_to_abstime", "9003", "i", "select pg_catalog.abstimein(pg_catalog.smalldatetime_out($1))", 702, []uint64{240, 9006}},
	12551: {"smalldatetime_to_time", "9003", "i", "select pg_catalog.time_in(pg_catalog.smalldatetime_out($1), 0::Oid, -1)", 1083, []uint64{1143, 9006}},
	12553: {"smalldatetime_to_timestamptz", "9003", "i", "select pg_catalog.timestamptz_in(pg_catalog.smalldatetime_out($1), 0::Oid, -1)", 1184, []uint64{1150, 9006}},
	12555: {"timestamptz_to_smalldatetime", "1184", "i", "select pg_catalog.smalldatetime_in(pg_catalog.TIMESTAMPTZ_OUT($1), 0::Oid, -1)", 9003, []uint64{9004, 1151}},
	12566: {"to_numeric", "3969", "i", "select pg_catalog.TO_NUMBER($1::TEXT)", 1700, []uint64{12478}},
	12568: {"to_integer", "3969", "i", "select pg_catalog.int4in(pg_catalog.nvarchar2out($1))", 23, []uint64{42, 3963}},
	12570: {"to_nvarchar2", "1114", "i", "select CAST(pg_catalog.timestamp_out($1) AS NVARCHAR2)", 3969, []uint64{1313}},
	12572: {"to_nvarchar2", "1186", "i", "select CAST(pg_catalog.interval_out($1) AS NVARCHAR2)", 3969, []uint64{1161}},
	12574: {"to_nvarchar2", "1700", "i", "select CAST(pg_catalog.numeric_out($1) AS NVARCHAR2)", 3969, []uint64{1702}},
	12575: {"to_nvarchar2", "21", "i", "select CAST(pg_catalog.int2out($1) AS NVARCHAR2)", 3969, []uint64{39}},
	12576: {"to_nvarchar2", "23", "i", "select CAST(pg_catalog.int4out($1) AS NVARCHAR2)", 3969, []uint64{43}},
	12577: {"to_nvarchar2", "20", "i", "select CAST(pg_catalog.int8out($1) AS NVARCHAR2)", 3969, []uint64{461}},
	12578: {"to_nvarchar2", "700", "i", "select CAST(pg_catalog.float4out($1) AS NVARCHAR2)", 3969, []uint64{201}},
	12579: {"to_nvarchar2", "701", "i", "select CAST(pg_catalog.float8out($1) AS NVARCHAR2)", 3969, []uint64{215}},
	12586: {"to_ts", "3969", "i", "select pg_catalog.timestamp_in(pg_catalog.nvarchar2out($1), 0::Oid, -1)", 1114, []uint64{1312, 3963}},
	12478: {"to_number", "25", "i", "select pg_catalog.numeric_in(pg_catalog.textout($1), 0::Oid, -1)", 1700, []uint64{1701, 47}},
}

type openGaussNativeCastTerminal struct {
	name, args, source, volatility string
	result                         uint64
}

var openGaussNativeCastTerminals = map[uint64]openGaussNativeCastTerminal{
	46:   {"textin", "2275", "textin", "i", 25},
	39:   {"int2out", "21", "int2out", "i", 2275},
	42:   {"int4in", "2275", "int4in", "i", 23},
	43:   {"int4out", "23", "int4out", "i", 2275},
	47:   {"textout", "25", "textout", "i", 2275},
	201:  {"float4out", "700", "float4out", "i", 2275},
	215:  {"float8out", "701", "float8out", "i", 2275},
	240:  {"abstimein", "2275", "abstimein", "s", 702},
	241:  {"abstimeout", "702", "abstimeout", "s", 2275},
	243:  {"reltimeout", "703", "reltimeout", "s", 2275},
	461:  {"int8out", "20", "int8out", "i", 2275},
	1045: {"bpcharout", "1042", "bpcharout", "i", 2275},
	1046: {"varcharin", "2275 26 23", "varcharin", "i", 1043},
	1047: {"varcharout", "1043", "varcharout", "i", 2275},
	1143: {"time_in", "2275 26 23", "time_in", "s", 1083},
	1144: {"time_out", "1083", "time_out", "i", 2275},
	1150: {"timestamptz_in", "2275 26 23", "timestamptz_in", "s", 1184},
	1151: {"timestamptz_out", "1184", "timestamptz_out", "s", 2275},
	1160: {"interval_in", "2275 26 23", "interval_in", "s", 1186},
	1161: {"interval_out", "1186", "interval_out", "i", 2275},
	1312: {"timestamp_in", "2275 26 23", "timestamp_in", "s", 1114},
	1313: {"timestamp_out", "1114", "timestamp_out", "s", 2275},
	1316: {"time", "1114", "timestamp_time", "i", 1083},
	1351: {"timetz_out", "1266", "timetz_out", "i", 2275},
	1475: {"polygon", "23 718", "circle_poly", "i", 604},
	1701: {"numeric_in", "2275 26 23", "numeric_in", "i", 1700},
	1702: {"numeric_out", "1700", "numeric_out", "i", 2275},
	2023: {"timestamp", "702", "abstime_timestamp", "s", 1114},
	2293: {"cstring_out", "2275", "cstring_out", "i", 2275},
	3963: {"nvarchar2out", "3969", "nvarchar2out", "i", 2275},
	3962: {"nvarchar2in", "2275 26 23", "nvarchar2in", "i", 3969},
	9004: {"smalldatetime_in", "2275 26 23", "smalldatetime_in", "s", 9003},
	9006: {"smalldatetime_out", "9003", "smalldatetime_out", "s", 2275},
}

func openGaussCheckNativeSQLCast(ctx context.Context, c *sql.Conn, oid, source, target, routine uint64) error {
	expected, ok := openGaussNativeSQLCasts[oid]
	if !ok || expected.source != source || expected.target != target || expected.routine != routine {
		return errOpenGaussDesignDeclaration
	}
	if err := openGaussCheckNativeCastEdge(ctx, c, oid, expected); err != nil {
		return err
	}
	return openGaussCheckNativeSQLRoutine(ctx, c, routine, map[uint64]bool{})
}

func openGaussCheckNativeCastEdge(ctx context.Context, c *sql.Conn, oid uint64, expected openGaussNativeSQLCast) error {
	var actual openGaussNativeSQLCast
	err := c.QueryRowContext(ctx, `SELECT castsource::bigint,casttarget::bigint,castfunc::bigint,castcontext::text,castmethod::text
 FROM pg_cast WHERE oid=$1::oid`, oid).Scan(&actual.source, &actual.target, &actual.routine, &actual.context, &actual.method)
	if err != nil || actual != expected {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	return ctx.Err()
}

// Fixed coercions inside the SQL bodies need their own catalog checks. In
// particular, timestamp_out(abstime) is not an exact argument match, while
// cstring-to-character casts have no pg_cast row and bind type I/O instead.
// These observations are not a lock against concurrent catalog replacement.
func openGaussCheckNativeSQLCastBindings(ctx context.Context, c *sql.Conn, routine uint64, checked map[uint64]bool) error {
	var edges []uint64
	switch routine {
	case 1364:
		edges = []uint64{11842, 11851}
	case 12547:
		edges = []uint64{11842}
		var unique bool
		if err := c.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname='pg_catalog' AND p.proname='timestamp_out' AND p.oid<>1313)`).Scan(&unique); err != nil || !unique {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
	case 12539:
		edges = []uint64{11807}
	case 12566:
		edges = []uint64{11811}
	}
	expectedEdges := map[uint64]openGaussNativeSQLCast{
		11731: {23, 26, 0, "i", "b"},
		11807: {1043, 25, 0, "i", "b"},
		11811: {3969, 25, 0, "i", "b"},
		11842: {702, 1114, 2023, "i", "f"},
		11851: {1114, 1083, 1316, "a", "f"},
	}
	if strings.Contains(openGaussNativeSQLRoutines[routine].body, "0::Oid") {
		edges = append(edges, 11731)
	}
	for _, oid := range edges {
		if err := openGaussCheckNativeCastEdge(ctx, c, oid, expectedEdges[oid]); err != nil {
			return err
		}
	}
	var target, input uint64
	var name string
	switch routine {
	case 12488, 12490, 12492, 12494, 12496:
		target, input, name = 25, 46, "text"
	case 12526, 12539, 12541:
		target, input, name = 1043, 1046, "varchar"
	case 12570, 12572, 12574, 12575, 12576, 12577, 12578, 12579:
		target, input, name = 3969, 3962, "nvarchar2"
	default:
		return ctx.Err()
	}
	var native bool
	err := c.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
 WHERE t.oid=$1::oid AND n.nspname='pg_catalog' AND t.typname=$2 AND t.typtype='b' AND t.typinput=$3::oid)
 AND EXISTS(SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
 WHERE t.oid=2275 AND n.nspname='pg_catalog' AND t.typname='cstring' AND t.typtype='p' AND t.typoutput=2293)
 AND NOT EXISTS(SELECT 1 FROM pg_cast WHERE castsource=2275 AND casttarget=$1::oid)`, target, name, input).Scan(&native)
	if err != nil || !native {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	for _, oid := range []uint64{input, 2293} {
		if err := openGaussCheckNativeSQLRoutine(ctx, c, oid, checked); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func openGaussCheckNativeSQLRoutine(ctx context.Context, c *sql.Conn, oid uint64, checked map[uint64]bool) error {
	if checked[oid] {
		return nil
	}
	if oid == 0 || oid >= 16384 || len(checked) >= 128 {
		return errOpenGaussDesignDeclaration
	}
	var name, args, namespace, source, volatility string
	var result, language uint64
	var unsafe bool
	err := c.QueryRowContext(ctx, `SELECT p.proname,p.proargtypes::text,n.nspname,p.prosrc,p.provolatile::text,
 p.prorettype::bigint,p.prolang::bigint,p.proisagg OR p.proiswindow OR p.proretset OR p.prosecdef OR p.proconfig IS NOT NULL
 OR NOT p.proisstrict OR p.probin IS NOT NULL OR p.pronargdefaults<>0 OR p.provariadic<>0 OR p.proallargtypes IS NOT NULL OR p.proargmodes IS NOT NULL
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid=$1::oid`, oid).Scan(&name, &args, &namespace, &source, &volatility, &result, &language, &unsafe)
	if err != nil || unsafe || namespace != "pg_catalog" || volatility != "i" && volatility != "s" {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	checked[oid] = true
	if expected, ok := openGaussNativeSQLRoutines[oid]; ok {
		body, e := schemaddl.OpenGaussCastBodySignature(source)
		wanted, wantedErr := schemaddl.OpenGaussCastBodySignature(expected.body)
		if language != 14 || name != expected.name || strings.Join(strings.Fields(args), " ") != expected.args || result != expected.result || volatility != expected.volatility || e != nil || wantedErr != nil || body != wanted {
			return fmt.Errorf("native SQL cast routine %d changed: %w", oid, errOpenGaussDesignDeclaration)
		}
		for _, callee := range expected.callees {
			if err := openGaussCheckNativeSQLRoutine(ctx, c, callee, checked); err != nil {
				return err
			}
		}
		return openGaussCheckNativeSQLCastBindings(ctx, c, oid, checked)
	}
	expected, ok := openGaussNativeCastTerminals[oid]
	if !ok || language != 12 || name != expected.name || strings.Join(strings.Fields(args), " ") != expected.args || result != expected.result || source != expected.source || volatility != expected.volatility {
		return errOpenGaussDesignDeclaration
	}
	return ctx.Err()
}
