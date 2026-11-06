package schemaddl

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PostgreSQLNodeBindings identifies only catalog references and explicitly
// bound, local expression variables. Callers bind OLD/NEW independently for
// triggers; query range tables and outer references cannot use table bindings.
// Ordinary constants, strings and unknown fields remain intact.
type PostgreSQLNodeBindings struct {
	Types, Relations map[int64]string
	Variables        map[int]map[int]string
	ByteOrder        string
	CompositeColumns map[int64]map[int]string
}

type pgNodeToken struct {
	kind     byte
	atom     string
	children []*pgNodeToken
}

type pgNodeField struct {
	name   string
	values []*pgNodeToken
}

// NormalizePostgreSQLNode normalizes public pg_node_tree output, retaining
// complete unknown fields and native constant payloads. It maps typed local
// references and known Var attribute numbers, and excludes parser locations.
func NormalizePostgreSQLNode(raw string, bindings PostgreSQLNodeBindings) (string, error) {
	if len(raw) > 1<<20 {
		return "", errors.New("PostgreSQL expression tree exceeds 1 MiB")
	}
	if !utf8.ValidString(raw) || strings.IndexByte(raw, 0) >= 0 {
		return "", errors.New("invalid PostgreSQL expression encoding")
	}
	tokens, err := pgParseNode(raw)
	if err != nil {
		return "", err
	}
	if len(tokens) > 1 || len(tokens) == 1 && tokens[0].kind != '{' && tokens[0].kind != '(' && !(tokens[0].kind == 'a' && tokens[0].atom == "<>") {
		return "", errors.New("invalid PostgreSQL expression root")
	}
	for _, token := range tokens {
		if err := pgNormalizeNode(token, bindings); err != nil {
			return "", err
		}
	}
	return Signature(tokens), nil
}

// JSON sees this explicit form, rather than unexported parser implementation
// fields. Node kinds distinguish reference markers from literal strings.
func (t pgNodeToken) MarshalJSON() ([]byte, error) {
	type value struct {
		Kind     byte           `json:"kind"`
		Atom     string         `json:"atom,omitempty"`
		Children []*pgNodeToken `json:"children,omitempty"`
	}
	return json.Marshal(value{t.kind, t.atom, t.children})
}

func pgParseNode(raw string) ([]*pgNodeToken, error) {
	at, count := 0, 0
	var sequence func(byte, int) ([]*pgNodeToken, error)
	sequence = func(end byte, depth int) ([]*pgNodeToken, error) {
		if depth > 128 {
			return nil, errors.New("PostgreSQL expression nesting exceeds 128")
		}
		var out []*pgNodeToken
		for at < len(raw) {
			ch := raw[at]
			if ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t' {
				at++
				continue
			}
			if ch == ')' || ch == '}' || ch == ']' {
				if ch != end {
					return nil, errors.New("unbalanced PostgreSQL expression tree")
				}
				at++
				return out, nil
			}
			count++
			if count > 65536 {
				return nil, errors.New("PostgreSQL expression token count exceeds 65536")
			}
			token := &pgNodeToken{kind: 'a'}
			if ch == '(' || ch == '{' || ch == '[' {
				token.kind = ch
				at++
				close := byte(')')
				if ch == '{' {
					close = '}'
				} else if ch == '[' {
					close = ']'
				}
				var err error
				token.children, err = sequence(close, depth+1)
				if err != nil {
					return nil, err
				}
			} else {
				start := at
				if ch == '"' {
					token.kind = 's'
					at++
					closed := false
					for at < len(raw) {
						if raw[at] == '\\' {
							at += 2
							continue
						}
						if raw[at] == '"' {
							at++
							closed = true
							break
						}
						at++
					}
					if !closed || at > len(raw) {
						return nil, errors.New("unterminated PostgreSQL expression string")
					}
				} else {
					for at < len(raw) {
						ch = raw[at]
						if ch == '\\' {
							token.kind = 's'
							at += 2
							continue
						}
						if strings.ContainsRune(" \t\r\n(){}[]", rune(ch)) {
							break
						}
						at++
					}
					if at > len(raw) || start == at {
						return nil, errors.New("invalid PostgreSQL expression atom")
					}
				}
				token.atom = raw[start:at]
			}
			out = append(out, token)
		}
		if end != 0 {
			return nil, errors.New("truncated PostgreSQL expression tree")
		}
		return out, nil
	}
	return sequence(0, 0)
}

func pgNodeFields(node *pgNodeToken) (string, []pgNodeField, error) {
	return pgNodeFieldsDialect(node, false)
}

func pgNodeFieldsDialect(node *pgNodeToken, native bool) (string, []pgNodeField, error) {
	if node.kind != '{' || len(node.children) < 1 || node.children[0].kind != 'a' {
		return "", nil, errors.New("invalid PostgreSQL expression node")
	}
	name := node.children[0].atom
	emptyCursor := func(field pgNodeField) bool { return native && name == "CONST" && field.name == ":cursor_data" }
	var fields []pgNodeField
	seen := map[string]bool{}
	for _, token := range node.children[1:] {
		if token.kind == 'a' && strings.HasPrefix(token.atom, ":") {
			if len(token.atom) == 1 || seen[token.atom] || len(fields) > 0 && len(fields[len(fields)-1].values) == 0 && !emptyCursor(fields[len(fields)-1]) {
				return "", nil, errors.New("invalid PostgreSQL expression field")
			}
			seen[token.atom] = true
			fields = append(fields, pgNodeField{name: token.atom})
		} else {
			if len(fields) == 0 {
				return "", nil, errors.New("unnamed PostgreSQL expression value")
			}
			fields[len(fields)-1].values = append(fields[len(fields)-1].values, token)
		}
	}
	if len(fields) > 0 && len(fields[len(fields)-1].values) == 0 && !emptyCursor(fields[len(fields)-1]) {
		return "", nil, errors.New("empty PostgreSQL expression field")
	}
	return name, fields, nil
}

func pgNodeInteger(values []*pgNodeToken) (int64, error) {
	if len(values) != 1 || values[0].kind != 'a' {
		return 0, errors.New("noninteger PostgreSQL expression reference")
	}
	n, err := strconv.ParseInt(values[0].atom, 10, 64)
	if err != nil {
		return 0, errors.New("invalid PostgreSQL expression integer")
	}
	return n, nil
}

func pgNormalizeNode(node *pgNodeToken, bindings PostgreSQLNodeBindings) error {
	if node.kind != '{' {
		for _, child := range node.children {
			if err := pgNormalizeNode(child, bindings); err != nil {
				return err
			}
		}
		return nil
	}
	name, fields, err := pgNodeFields(node)
	if err != nil {
		return err
	}
	// PostgreSQL 14 uses short names for these expression nodes. Only
	// reviewed aliases gain typed bindings and parser-location normalization.
	if alias := map[string]string{"ARRAY": "ARRAYEXPR", "CASE": "CASEEXPR", "WHEN": "CASEWHEN"}[name]; alias != "" {
		name = alias
		node.children[0].atom = name
	}
	byName := map[string][]*pgNodeToken{}
	for _, f := range fields {
		byName[f.name] = f.values
	}
	if name == "CONST" {
		if err := pgNormalizeOIDConstant(byName, bindings); err != nil {
			return err
		}
	}
	varno := int64(0)
	if name == "VAR" {
		varno, err = pgNodeInteger(byName[":varno"])
		if err != nil || varno <= 0 || bindings.Variables[int(varno)] == nil {
			return errors.New("unbound PostgreSQL expression relation variable")
		}
		level, err := pgNodeInteger(byName[":varlevelsup"])
		if err != nil || level != 0 {
			return errors.New("unbound PostgreSQL outer expression variable")
		}
	}
	if name == "FIELDSELECT" && len(byName[":arg"]) == 1 && byName[":arg"][0].kind == '{' {
		argName, argFields, err := pgNodeFields(byName[":arg"][0])
		if err != nil {
			return err
		}
		argTypeField := map[string]string{"VAR": ":vartype", "CONST": ":consttype", "PARAM": ":paramtype", "FUNCEXPR": ":funcresulttype", "ROWEXPR": ":row_typeid", "RELABELTYPE": ":resulttype"}[argName]
		for _, f := range argFields {
			if f.name != argTypeField {
				continue
			}
			typeID, err := pgNodeInteger(f.values)
			if err != nil {
				return err
			}
			if columns, ok := bindings.CompositeColumns[typeID]; ok {
				n, err := pgNodeInteger(byName[":fieldnum"])
				if err != nil || columns[int(n)] == "" {
					return errors.New("unbound PostgreSQL composite attribute")
				}
				byName[":fieldnum"][0].kind, byName[":fieldnum"][0].atom = 'c', columns[int(n)]
			}
		}
	}
	if name == "FIELDSTORE" {
		typeID, err := pgNodeInteger(byName[":resulttype"])
		if err != nil {
			return err
		}
		if columns, ok := bindings.CompositeColumns[typeID]; ok {
			values := byName[":fieldnums"]
			if len(values) != 1 || values[0].kind != '(' || len(values[0].children) == 0 || values[0].children[0].kind != 'a' || values[0].children[0].atom != "i" {
				return errors.New("invalid PostgreSQL composite field list")
			}
			for _, value := range values[0].children[1:] {
				n, err := pgNodeInteger([]*pgNodeToken{value})
				if err != nil || columns[int(n)] == "" {
					return errors.New("unbound PostgreSQL composite attribute")
				}
				value.kind, value.atom = 'c', columns[int(n)]
			}
		}
	}
	known := pgKnownExpressionNode(name)
	for _, field := range fields {
		var references map[int64]string
		switch {
		case known && pgNodeTypeField(name, field.name):
			references = bindings.Types
		case name == "TARGETENTRY" && field.name == ":resorigtbl" || name == "RANGETBLENTRY" && field.name == ":relid" || name == "NEXTVALUEEXPR" && field.name == ":seqid":
			references = bindings.Relations
		case pgNodeHasLocation(name) && field.name == ":location":
			if _, err := pgNodeInteger(field.values); err != nil {
				return err
			}
			field.values[0].kind, field.values[0].atom = 'l', "parser-location"
		case pgNodeCoercionField(name, field.name):
			form, err := pgNodeInteger(field.values)
			if err != nil || form < 0 || form > 3 {
				return errors.New("unknown PostgreSQL expression coercion form")
			}
			// CoercionForm chooses how ruleutils prints an already resolved
			// operation. pg_get_expr can turn an implicit coercion into an
			// explicit cast on reparse; function/type/args still prove meaning.
			field.values[0].kind, field.values[0].atom = 'f', "coercion-display"
		case name == "VAR" && (field.name == ":varattno" || field.name == ":varattnosyn"):
			if varno != 0 {
				variable := varno
				if field.name == ":varattnosyn" {
					variable, err = pgNodeInteger(byName[":varnosyn"])
					if err != nil || variable < 0 {
						return errors.New("invalid PostgreSQL syntactic expression variable")
					}
				}
				n, err := pgNodeInteger(field.values)
				if err != nil {
					return err
				}
				if n > 0 {
					column := bindings.Variables[int(variable)][int(n)]
					if column == "" {
						return errors.New("unbound PostgreSQL expression attribute")
					}
					field.values[0].kind, field.values[0].atom = 'c', column
				}
			}
		}
		if references != nil {
			n, err := pgNodeInteger(field.values)
			if err != nil {
				return err
			}
			if key, ok := references[n]; ok {
				field.values[0].kind, field.values[0].atom = 'r', key
			}
		}
		for _, child := range field.values {
			if err := pgNormalizeNode(child, bindings); err != nil {
				return err
			}
		}
	}
	return nil
}

func pgKnownExpressionNode(name string) bool {
	switch name {
	case "VAR", "CONST", "PARAM", "FUNCEXPR", "OPEXPR", "DISTINCTEXPR", "NULLIFEXPR", "SCALARARRAYOPEXPR", "BOOLEXPR", "RELABELTYPE", "COERCEVIAIO", "ARRAYCOERCEEXPR", "COERCETODOMAIN", "COERCETODOMAINVALUE", "CONVERTROWTYPEEXPR", "ROWEXPR", "ARRAYEXPR", "CASEEXPR", "CASEWHEN", "CASETESTEXPR", "COALESCEEXPR", "MINMAXEXPR", "SQLVALUEFUNCTION", "XMLEXPR", "NULLTEST", "BOOLEANTEST", "COLLATEEXPR", "FIELDSELECT", "FIELDSTORE", "SUBSCRIPTINGREF", "ROWCOMPAREEXPR", "AGGREF", "GROUPINGFUNC", "WINDOWFUNC", "SETTODEFAULT", "NEXTVALUEEXPR", "INFERENCEELEM", "TARGETENTRY", "RANGETBLENTRY":
		return true
	}
	return false
}

func pgNodeHasLocation(name string) bool {
	switch name {
	case "VAR", "CONST", "PARAM", "AGGREF", "GROUPINGFUNC", "WINDOWFUNC", "FUNCEXPR", "OPEXPR", "DISTINCTEXPR", "NULLIFEXPR", "SCALARARRAYOPEXPR", "BOOLEXPR", "RELABELTYPE", "COERCEVIAIO", "ARRAYCOERCEEXPR", "CONVERTROWTYPEEXPR", "ARRAYEXPR", "ROWEXPR", "CASEEXPR", "CASEWHEN", "COALESCEEXPR", "MINMAXEXPR", "SQLVALUEFUNCTION", "XMLEXPR", "NULLTEST", "BOOLEANTEST", "COLLATEEXPR", "COERCETODOMAIN", "COERCETODOMAINVALUE", "SETTODEFAULT":
		return true
	}
	return false
}

func pgNodeCoercionField(node, field string) bool {
	switch node {
	case "FUNCEXPR":
		return field == ":funcformat"
	case "RELABELTYPE":
		return field == ":relabelformat"
	case "COERCEVIAIO", "ARRAYCOERCEEXPR":
		return field == ":coerceformat"
	case "ROWEXPR":
		return field == ":row_format"
	case "CONVERTROWTYPEEXPR":
		return field == ":convertformat"
	case "COERCETODOMAIN":
		return field == ":coercionformat"
	}
	return false
}

func pgNodeTypeField(node, field string) bool {
	switch node {
	case "VAR":
		return field == ":vartype"
	case "CONST":
		return field == ":consttype"
	case "PARAM":
		return field == ":paramtype"
	case "FUNCEXPR":
		return field == ":funcresulttype"
	case "OPEXPR", "DISTINCTEXPR", "NULLIFEXPR":
		return field == ":opresulttype"
	case "AGGREF":
		return field == ":aggtype"
	case "WINDOWFUNC":
		return field == ":wintype"
	case "COALESCEEXPR":
		return field == ":coalescetype"
	case "MINMAXEXPR":
		return field == ":minmaxtype"
	case "CASEEXPR":
		return field == ":casetype"
	case "ROWEXPR":
		return field == ":row_typeid"
	case "ARRAYEXPR":
		return field == ":array_typeid" || field == ":element_typeid"
	case "RELABELTYPE", "COERCEVIAIO", "ARRAYCOERCEEXPR", "COERCETODOMAIN", "CONVERTROWTYPEEXPR", "FIELDSELECT", "FIELDSTORE":
		return field == ":resulttype"
	case "COERCETODOMAINVALUE", "CASETESTEXPR", "SETTODEFAULT", "NEXTVALUEEXPR":
		return field == ":typeId"
	case "SQLVALUEFUNCTION", "XMLEXPR":
		return field == ":type"
	case "SUBSCRIPTINGREF":
		return field == ":refcontainertype" || field == ":refelemtype" || field == ":refrestype"
	}
	return false
}

func pgNormalizeOIDConstant(fields map[string][]*pgNodeToken, bindings PostgreSQLNodeBindings) error {
	ctype, err := pgNodeInteger(fields[":consttype"])
	if err != nil {
		return err
	}
	refs := bindings.Relations
	if ctype == 2206 { // regtype
		refs = bindings.Types
	} else if ctype != 2205 { // regclass
		return nil
	}
	length, err := pgNodeInteger(fields[":constlen"])
	if err != nil || length != 4 || len(fields[":constbyval"]) != 1 || fields[":constbyval"][0].atom != "true" {
		return errors.New("invalid PostgreSQL OID constant representation")
	}
	if len(fields[":constisnull"]) == 1 && fields[":constisnull"][0].atom == "true" {
		return nil
	}
	values := fields[":constvalue"]
	if len(values) != 2 || values[0].kind != 'a' || values[0].atom != "4" || values[1].kind != '[' {
		return errors.New("invalid PostgreSQL OID constant datum")
	}
	data := values[1].children
	if len(data) != 4 && len(data) != 8 {
		return errors.New("unsupported PostgreSQL OID datum width")
	}
	var raw []byte
	for _, atom := range data {
		n, err := pgNodeInteger([]*pgNodeToken{atom})
		if err != nil || n < -128 || n > 255 {
			return errors.New("invalid PostgreSQL OID datum byte")
		}
		raw = append(raw, byte(n))
	}
	var oid uint32
	switch bindings.ByteOrder {
	case "little":
		oid = binary.LittleEndian.Uint32(raw[:4])
	case "big":
		oid = binary.BigEndian.Uint32(raw[len(raw)-4:])
	default:
		return errors.New("PostgreSQL native byte order is required for OID constants")
	}
	if key, ok := refs[int64(oid)]; ok {
		values[1].kind, values[1].atom, values[1].children = 'r', key, nil
	}
	return nil
}
