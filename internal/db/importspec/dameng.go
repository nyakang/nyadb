package importspec

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var damengType = regexp.MustCompile(`^(CHAR|VARCHAR|BINARY|VARBINARY|DECIMAL|NUMERIC|NUMBER|DEC|TIME|TIMESTAMP)(?: ?\( ?([0-9]+) ?(?:, ?([0-9]+) ?)?\))?( WITH TIME ZONE)?$`)

func damengColumns(cols []contract.DataImportColumn) ([]contract.StructureColumn, error) {
	if len(cols) == 0 || len(cols) > 128 {
		return nil, contract.ErrDataPage
	}
	out := make([]contract.StructureColumn, len(cols))
	seen := map[string]bool{}
	for i, in := range cols {
		if Identifier("dameng", in.Name) != nil || len(in.Name) > 128 || seen[in.Name] || strings.EqualFold(in.Name, "ROWID") {
			return nil, contract.ErrDataPage
		}
		seen[in.Name] = true
		typ := strings.Join(strings.Fields(strings.ToUpper(in.Type)), " ")
		c := contract.StructureColumn{Name: in.Name, Position: int64(i + 1), Nullable: in.Nullable && !in.PrimaryKey, ValueKind: contract.Text}
		if in.PrimaryKey {
			c.Key = "PRI"
		}
		switch typ {
		case "INTEGER", "INT", "BIGINT", "SMALLINT", "TINYINT":
			if typ == "INTEGER" {
				typ = "INT"
			}
			c.DataType, c.ValueKind = typ, contract.Integer
		case "BIT", "BOOLEAN":
			typ = "BIT"
			c.DataType, c.ValueKind = typ, contract.Boolean
		case "DOUBLE", "REAL", "FLOAT":
			c.DataType, c.ValueKind = typ, contract.Floating
		case "CLOB", "TEXT":
			typ, c.DataType = "CLOB", "CLOB"
		case "BLOB", "IMAGE":
			typ, c.DataType, c.ValueKind = "BLOB", "BLOB", contract.Binary
		case "DATE":
			c.DataType, c.ValueKind = typ, contract.DateTime
		default:
			m := damengType.FindStringSubmatch(typ)
			if m == nil {
				return nil, contract.ErrDataPage
			}
			n, _ := strconv.ParseInt(m[2], 10, 64)
			scale, _ := strconv.ParseInt(m[3], 10, 64)
			c.DataType = m[1]
			switch m[1] {
			case "CHAR", "VARCHAR", "BINARY", "VARBINARY":
				if n < 1 || n > 8000 || m[3] != "" || m[4] != "" {
					return nil, contract.ErrDataPage
				}
				typ = m[1] + "(" + strconv.FormatInt(n, 10) + ")"
				if m[1] == "CHAR" || m[1] == "VARCHAR" {
					c.CharacterMaxLength = &n
				} else {
					c.ValueKind = contract.Binary
				}
			case "DECIMAL", "NUMERIC", "NUMBER", "DEC":
				if m[2] == "" {
					n = 38
				}
				if n < 1 || n > 38 || scale < 0 || scale > n || m[4] != "" {
					return nil, contract.ErrDataPage
				}
				c.DataType, c.ValueKind, c.NumericPrecision, c.NumericScale = "DECIMAL", contract.Decimal, &n, &scale
				typ = "DECIMAL(" + strconv.FormatInt(n, 10) + "," + strconv.FormatInt(scale, 10) + ")"
			case "TIME", "TIMESTAMP":
				if m[2] == "" {
					n = 6
				}
				if n < 0 || n > 6 || m[3] != "" {
					return nil, contract.ErrDataPage
				}
				c.ValueKind, c.DatetimePrecision = contract.DateTime, &n
				c.DataType += m[4]
				typ = m[1] + "(" + strconv.FormatInt(n, 10) + ")" + m[4]
			}
		}
		if in.PrimaryKey && (c.DataType == "CLOB" || c.DataType == "BLOB") {
			return nil, contract.ErrDataPage
		}
		c.Type = typ
		out[i] = c
	}
	return out, nil
}

func damengValidate(metadata []contract.StructureColumn, r contract.DataImportRequest) error {
	var err error
	r, err = Own(r)
	if err != nil {
		return err
	}
	if r.NewTable {
		metadata, err = damengColumns(r.Columns)
		if err != nil {
			return err
		}
	}
	allowed := map[string]contract.StructureColumn{}
	for _, c := range metadata {
		allowed[c.Name] = c
	}
	if len(allowed) == 0 {
		return contract.ErrDataPage
	}
	if NeedsKey(r.Mode) {
		if len(r.MatchColumns) == 0 || len(r.MatchColumns) > 16 {
			return contract.ErrDataReadOnly
		}
		seen := map[string]bool{}
		for _, k := range r.MatchColumns {
			if _, ok := allowed[k]; !ok || seen[k] {
				return contract.ErrDataReadOnly
			}
			seen[k] = true
		}
	}
	for _, e := range r.Edits {
		for name, v := range e.Values {
			c, ok := allowed[name]
			validDefault := v.Default && !v.Null && !v.Binary && v.Kind == "" && v.Text == "" && len(v.Bytes) == 0
			identityLookup := c.Extra == "IDENTITY" && NeedsKey(r.Mode) && slices.Contains(r.MatchColumns, name) && !v.Default
			if !ok || contract.DataColumnGenerated(c) && !(c.Extra == "IDENTITY" && validDefault || identityLookup) || v.Default && !validDefault || v.Null && v.Binary || v.Binary && v.Text != "" || !v.Binary && len(v.Bytes) > 0 || v.Null && (v.Text != "" || len(v.Bytes) > 0) || !utf8.ValidString(v.Text) {
				return contract.ErrDataPage
			}
			if len(v.Text) > contract.DataEditMaxBytes || len(v.Bytes) > contract.DataEditMaxBytes {
				return contract.ErrDataEditLimit
			}
		}
		for _, name := range r.MatchColumns {
			v, ok := e.Values[name]
			if !ok || v.Null || v.Default {
				return contract.ErrDataReadOnly
			}
		}
	}
	return nil
}
