package schemaddl

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func pgNodeTestBindings() PostgreSQLNodeBindings {
	return PostgreSQLNodeBindings{
		Types:            map[int64]string{80001: "row", 80002: "array"},
		Relations:        map[int64]string{70001: "table", 70002: "sequence"},
		Variables:        map[int]map[int]string{1: {1: "id", 4: "value"}, 2: {1: "id", 4: "value"}},
		CompositeColumns: map[int64]map[int]string{80001: {1: "id", 4: "value"}},
		ByteOrder:        "little",
	}
}

func pgNodeHash(t *testing.T, raw string, bindings PostgreSQLNodeBindings) string {
	t.Helper()
	hash, err := NormalizePostgreSQLNode(raw, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestPostgreSQLNodeBindsTypedReferencesAcrossPhysicalIdentityAndAttributeGaps(t *testing.T) {
	a := pgNodeTestBindings()
	b := PostgreSQLNodeBindings{
		Types:            map[int64]string{81001: "row", 81002: "array"},
		Relations:        map[int64]string{71001: "table", 71002: "sequence"},
		Variables:        map[int]map[int]string{1: {1: "id", 2: "value"}, 2: {1: "id", 2: "value"}},
		CompositeColumns: map[int64]map[int]string{81001: {1: "id", 2: "value"}},
		ByteOrder:        "little",
	}
	varNode := `{VAR :varno 1 :varattno 4 :vartype 80001 :vartypmod -1 :varcollid 0 :varlevelsup 0 :varnosyn 1 :varattnosyn 4 :location 91}`
	for _, left := range []string{
		varNode,
		`{FUNCEXPR :funcid 23 :funcresulttype 80001 :args (` + varNode + `) :location 9}`,
		`{FIELDSELECT :arg {VAR :varno 1 :varattno 0 :vartype 80001 :varlevelsup 0 :varnosyn 1 :varattnosyn 0 :location -1} :fieldnum 4 :resulttype 25 :resulttypmod -1 :resultcollid 100}`,
		`{FIELDSTORE :arg ` + varNode + ` :newvals (` + varNode + `) :fieldnums (i 4) :resulttype 80001}`,
		`{NEXTVALUEEXPR :seqid 70002 :typeId 80001}`,
		`({TARGETENTRY :expr ` + varNode + ` :resorigtbl 70001 :resorigcol 0})`,
		`{ARRAYEXPR :array_typeid 80002 :element_typeid 80001 :elements (` + varNode + `) :location 9}`,
	} {
		right := strings.NewReplacer("80001", "81001", "80002", "81002", "70001", "71001", "70002", "71002", ":varattno 4", ":varattno 2", ":varattnosyn 4", ":varattnosyn 2", ":fieldnum 4", ":fieldnum 2", "(i 4)", "(i 2)", ":location 91", ":location 19", ":location 9}", ":location 99}").Replace(left)
		if pgNodeHash(t, left, a) != pgNodeHash(t, right, b) {
			t.Fatalf("typed references did not normalize: %s", left)
		}
	}
	before := Signature(a)
	_ = pgNodeHash(t, varNode, a)
	if Signature(a) != before {
		t.Fatal("expression normalization modified caller bindings")
	}
}

func TestPostgreSQLNodePreservesUnknownFieldsLiteralPayloadsAndVariableRoles(t *testing.T) {
	b := pgNodeTestBindings()
	for _, pair := range [][2]string{
		{`{CONST :consttype 23 :constlen 4 :constbyval true :constisnull false :constvalue 4 [ 1 0 0 0 0 0 0 0 ] :location 5}`, `{CONST :consttype 23 :constlen 4 :constbyval true :constisnull false :constvalue 4 [ 2 0 0 0 0 0 0 0 ] :location 5}`},
		{`{CUSTOMNODE :type 80001 :location 1}`, `{CUSTOMNODE :type 81001 :location 1}`},
		{`{CUSTOMNODE :location 1}`, `{CUSTOMNODE :location 2}`},
		{`{FIELDSELECT :new_type 80001}`, `{FIELDSELECT :new_type 81001}`},
		{`{FIELDSELECT :location 1}`, `{FIELDSELECT :location 2}`},
		{`{CONST :consttype 25 :constvalue 3 [ 97 98 99 ] :location 1}`, `{CONST :consttype 25 :constvalue 3 [ 97 98 100 ] :location 1}`},
		{`{CUSTOMNODE :value "70001"}`, `{CUSTOMNODE :value "71001"}`},
		{`{CUSTOMNODE :value escaped\ name}`, `{CUSTOMNODE :value "escaped name"}`},
		{`{VAR :varno 1 :varattno 4 :vartype 25 :varlevelsup 0 :varnosyn 1 :varattnosyn 4 :location 1}`, `{VAR :varno 2 :varattno 4 :vartype 25 :varlevelsup 0 :varnosyn 2 :varattnosyn 4 :location 1}`},
		{`{VAR :varno 1 :varattno 4 :vartype 25 :varlevelsup 0 :varnosyn 1 :varattnosyn 4 :location 1}`, `{VAR :varno 1 :varattno 1 :vartype 25 :varlevelsup 0 :varnosyn 1 :varattnosyn 1 :location 1}`},
	} {
		if pgNodeHash(t, pair[0], b) == pgNodeHash(t, pair[1], b) {
			t.Fatalf("semantic or opaque property disappeared: %s", pair[0])
		}
	}
	if pgNodeHash(t, " ( {CUSTOMNODE :value \"escaped\\\" quote\"} ) ", b) != pgNodeHash(t, "({CUSTOMNODE :value \"escaped\\\" quote\"})", b) {
		t.Fatal("insignificant token spacing changed identity")
	}
}

func TestPostgreSQLNodeBindsLegacyArrayTypesAndPreservesContents(t *testing.T) {
	a, b := pgNodeTestBindings(), pgNodeTestBindings()
	b.Types = map[int64]string{81001: "row", 81002: "array"}
	legacy := `{ARRAY :array_typeid 80002 :element_typeid 80001 :elements ({CONST :consttype 80001 :constvalue 4 [ 1 0 0 0 ] :location 5}) :multidims false :location 10}`
	current := strings.NewReplacer("{ARRAY ", "{ARRAYEXPR ", "80001", "81001", "80002", "81002", ":location 5", ":location 50", ":location 10", ":location 100").Replace(legacy)
	if pgNodeHash(t, legacy, a) != pgNodeHash(t, current, b) {
		t.Fatal("legacy array retained physical types or parser locations")
	}
	for _, changed := range []string{
		strings.Replace(legacy, "[ 1 0 0 0 ]", "[ 2 0 0 0 ]", 1),
		strings.Replace(legacy, ":multidims false", ":multidims true", 1),
		strings.Replace(legacy, ":location 10", ":unknown 1 :location 10", 1),
	} {
		if pgNodeHash(t, legacy, a) == pgNodeHash(t, changed, a) {
			t.Fatal("legacy array discarded values or unknown properties")
		}
	}
}

func TestPostgreSQLNodeCoercionDisplayDoesNotChangeResolvedOperation(t *testing.T) {
	b := pgNodeTestBindings()
	for node, field := range map[string]string{"FUNCEXPR": "funcformat", "RELABELTYPE": "relabelformat", "COERCEVIAIO": "coerceformat", "ARRAYCOERCEEXPR": "coerceformat", "ROWEXPR": "row_format", "CONVERTROWTYPEEXPR": "convertformat", "COERCETODOMAIN": "coercionformat"} {
		implicit := fmt.Sprintf(`{%s :%s 2 :args ({CONST :consttype 23 :constvalue 4 [ 1 0 0 0 ]})}`, node, field)
		explicit := strings.Replace(implicit, ":"+field+" 2", ":"+field+" 1", 1)
		if pgNodeHash(t, implicit, b) != pgNodeHash(t, explicit, b) {
			t.Fatal("deparser coercion display changed resolved meaning", node)
		}
		unknown := strings.Replace(implicit, ":"+field+" 2", ":"+field+" 4", 1)
		if _, err := NormalizePostgreSQLNode(unknown, b); err == nil {
			t.Fatal("unknown coercion form acquired normalization permission", node)
		}
	}
	if pgNodeHash(t, `{FUNCEXPR :funcid 100 :funcformat 1}`, b) == pgNodeHash(t, `{FUNCEXPR :funcid 101 :funcformat 2}`, b) {
		t.Fatal("coercion display normalization hid a different function")
	}
	if pgNodeHash(t, `{FIELDSELECT :funcformat 1}`, b) == pgNodeHash(t, `{FIELDSELECT :funcformat 2}`, b) {
		t.Fatal("unknown node field acquired coercion normalization permission")
	}
}

func pgTestOIDConstant(ctype int, oid uint32, order string, width int) string {
	raw := make([]byte, width)
	if order == "big" {
		binary.BigEndian.PutUint32(raw[width-4:], oid)
	} else {
		binary.LittleEndian.PutUint32(raw[:4], oid)
	}
	var values []string
	for _, b := range raw {
		values = append(values, fmt.Sprint(int8(b)))
	}
	return fmt.Sprintf(`{CONST :consttype %d :constlen 4 :constbyval true :constisnull false :constvalue 4 [ %s ] :location 1}`, ctype, strings.Join(values, " "))
}

func TestPostgreSQLNodeOIDDatumsUseCapturedServerOrderWithoutChangingExternalValues(t *testing.T) {
	for _, ctype := range []int{2205, 2206} {
		for _, order := range []string{"little", "big"} {
			for _, width := range []int{4, 8} {
				a, b := pgNodeTestBindings(), pgNodeTestBindings()
				a.ByteOrder, b.ByteOrder = order, order
				a.Types[0xfedcba98], a.Relations[0xfedcba98] = "local", "local"
				b.Types[0x12345678], b.Relations[0x12345678] = "local", "local"
				if pgNodeHash(t, pgTestOIDConstant(ctype, 0xfedcba98, order, width), a) != pgNodeHash(t, pgTestOIDConstant(ctype, 0x12345678, order, width), b) {
					t.Fatal("local OID constants retained physical identities", ctype, order, width)
				}
				if pgNodeHash(t, pgTestOIDConstant(ctype, 123, order, width), a) == pgNodeHash(t, pgTestOIDConstant(ctype, 124, order, width), a) {
					t.Fatal("external OID constant acquired replacement permission")
				}
			}
		}
	}
	b := pgNodeTestBindings()
	b.ByteOrder = ""
	if _, err := NormalizePostgreSQLNode(pgTestOIDConstant(2205, 70001, "little", 8), b); err == nil {
		t.Fatal("unknown native order silently guessed")
	}
	_ = pgNodeHash(t, `{CONST :consttype 2205 :constlen 4 :constbyval true :constisnull true :constvalue <> :location -1}`, b)
}

func TestPostgreSQLNodeRejectsMalformedUnboundAndOversizedTrees(t *testing.T) {
	b := pgNodeTestBindings()
	for _, raw := range []string{
		`{`, `() []`, `{VAR :varno}`, `{VAR :varno 1 :varno 2}`, `{VAR value}`, `{VAR :varno 1 :}`, `{VAR :varno 1)}`, `{VAR :text "unterminated}`, `{VAR :text bad\}`, `word`, `[]`,
		`{VAR :varno 3 :varattno 1 :varlevelsup 0}`, `{VAR :varno 1 :varattno 2 :varlevelsup 0}`, `{VAR :varno 1 :varattno 1 :varlevelsup 1}`, `{VAR :varno 1 :varattno 1}`, `{VAR :varno 1 :varattno 1 :varlevelsup 0 :varnosyn 3 :varattnosyn 1}`,
		`{FIELDSELECT :arg {CONST :consttype 80001} :fieldnum 2 :resulttype 23}`, `{FIELDSTORE :resulttype 80001 :fieldnums (i 2)}`, `{FIELDSTORE :resulttype 80001 :fieldnums (i "4")}`,
		`{CONST :consttype 2205 :constlen 4 :constbyval false :constisnull false :constvalue 4 [ 1 0 0 0 ]}`,
		`{CONST :consttype 2205 :constlen 4 :constbyval true :constisnull false :constvalue 4 [ 256 0 0 0 ]}`,
		`{CONST :consttype 2205 :constlen 4 :constbyval true :constisnull false :constvalue 4 [ 1 0 0 ]}`,
		strings.Repeat("(", 130) + strings.Repeat(")", 130), "(" + strings.Repeat("1 ", 65537) + ")", strings.Repeat(" ", (1<<20)+1), "{CUSTOMNODE :text \"\x00\"}", string([]byte{'{', 0xff, '}'}),
	} {
		if signature, err := NormalizePostgreSQLNode(raw, b); err == nil || signature != "" {
			t.Fatalf("unprovable expression accepted: %q", raw[:min(len(raw), 100)])
		}
	}
	_ = pgNodeHash(t, "", b)
	_ = pgNodeHash(t, "<>", b)
}

func FuzzPostgreSQLNode(f *testing.F) {
	for _, raw := range []string{"", "<>", "()", `{CONST :consttype 23 :constvalue 4 [ 1 2 3 4 ]}`, `{VAR :varno 1 :varattno 1 :varlevelsup 0}`, `{FIELDSTORE :resulttype 80001 :fieldnums (i 4)}`} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		b := pgNodeTestBindings()
		first, err := NormalizePostgreSQLNode(raw, b)
		second, again := NormalizePostgreSQLNode(raw, b)
		if first != second || (err == nil) != (again == nil) {
			t.Fatal("expression result is not deterministic")
		}
	})
}

func TestPostgreSQLNodeMalformedFieldErrorDoesNotPrintExpressionText(t *testing.T) {
	_, err := NormalizePostgreSQLNode(`{CONST :consttype private_expression_value}`, pgNodeTestBindings())
	if err == nil || strings.Contains(err.Error(), "private_expression_value") {
		t.Fatal("malformed reference text exposed in error", err)
	}
}

func TestPostgreSQLNodeBindsLegacyCaseAndPreservesBranches(t *testing.T) {
	b := pgNodeTestBindings()
	legacy := `{CASE :casetype 80001 :casecollid 0 :arg <> :args ({WHEN :expr {CONST :consttype 16 :constvalue 1 [ 0 ] :location 12} :result {CONST :consttype 80001 :constvalue 1 [ 1 ] :location 18} :location 10}) :defresult <> :location 8}`
	current := strings.NewReplacer("{CASE ", "{CASEEXPR ", "{WHEN ", "{CASEWHEN ", ":location 12", ":location 120", ":location 18", ":location 180", ":location 10", ":location 100", ":location 8", ":location 80").Replace(legacy)
	if pgNodeHash(t, legacy, b) != pgNodeHash(t, current, b) {
		t.Fatal("legacy CASE retained parser offsets")
	}
	if pgNodeHash(t, legacy, b) == pgNodeHash(t, strings.Replace(legacy, "[ 1 ]", "[ 2 ]", 1), b) {
		t.Fatal("CASE result value was discarded")
	}
}
