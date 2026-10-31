package mysqlddl

import (
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type DefaultKind string

const (
	NoDefault         DefaultKind = "none"
	NullDefault       DefaultKind = "null"
	LiteralDefault    DefaultKind = "literal"
	ExpressionDefault DefaultKind = "expression"
)

type Field struct {
	Name, Type                                               string
	Nullable                                                 bool
	DefaultKind                                              DefaultKind
	Default                                                  string
	Generation, Storage, Charset, Collation, Comment, Extras string
	AutoIncrement                                            bool
	Unsupported                                              bool
}

// ParseColumn understands common MySQL attributes. Unknown attributes leave
// the field readable but locked; other fields may still be edited losslessly.
func ParseColumn(raw string) (Field, error) {
	ts, e := significant(raw)
	if e != nil {
		return Field{}, e
	}
	if len(ts) < 2 || !strings.HasPrefix(ts[0].Text, "`") {
		return Field{}, errors.New("field name must be quoted")
	}
	f := Field{Name: Name(ts[0]), Nullable: true, DefaultKind: NoDefault}
	all, _ := sqltext.Tokens(raw)
	for _, t := range all {
		if t.Comment {
			f.Unsupported = true
		}
	}
	i := 2
	if i < len(ts) && ts[i].Text == "(" {
		end, e := balanced(ts, i)
		if e != nil {
			return f, e
		}
		i = end
	}
	for i < len(ts) && (sqltext.Word(ts[i], "UNSIGNED") || sqltext.Word(ts[i], "ZEROFILL") || sqltext.Word(ts[i], "SIGNED")) {
		i++
	}
	f.Type = raw[ts[1].Start:ts[i-1].End]
	var extras []string
	for i < len(ts) {
		start := i
		switch {
		case sqltext.Word(ts[i], "NOT") && i+1 < len(ts) && sqltext.Word(ts[i+1], "NULL"):
			f.Nullable = false
			i += 2
		case sqltext.Word(ts[i], "NULL"):
			f.Nullable = true
			i++
		case sqltext.Word(ts[i], "DEFAULT"):
			i++
			if i >= len(ts) {
				return f, errors.New("default value is missing")
			}
			value := i
			if ts[i].Text == "(" {
				i, e = balanced(ts, i)
			} else {
				i++
				if i < len(ts) && ts[i].Text == "(" {
					i, e = balanced(ts, i)
				}
				if i < len(ts) && ts[value].Text == "-" {
					i++
				}
			}
			if e != nil {
				return f, e
			}
			f.Default = raw[ts[value].Start:ts[i-1].End]
			f.DefaultKind = ExpressionDefault
			if sqltext.Word(ts[value], "NULL") {
				f.DefaultKind = NullDefault
				f.Default = ""
			} else if i == value+1 && strings.HasPrefix(ts[value].Text, "'") {
				f.DefaultKind = LiteralDefault
				f.Default = decodeLiteral(ts[value].Text)
			}
		case sqltext.Word(ts[i], "GENERATED") || sqltext.Word(ts[i], "AS"):
			for i < len(ts) && !sqltext.Word(ts[i], "AS") {
				i++
			}
			i++
			if i >= len(ts) || ts[i].Text != "(" {
				return f, errors.New("generated expression is missing")
			}
			value := i
			i, e = balanced(ts, i)
			if e != nil {
				return f, e
			}
			f.Generation = raw[ts[value].End:ts[i-1].Start]
			if i < len(ts) && (sqltext.Word(ts[i], "VIRTUAL") || sqltext.Word(ts[i], "STORED")) {
				f.Storage = strings.ToUpper(ts[i].Text)
				i++
			}
		case sqltext.Word(ts[i], "CHARACTER"):
			if i+2 >= len(ts) || !sqltext.Word(ts[i+1], "SET") {
				return f, errors.New("invalid character set")
			}
			f.Charset = ts[i+2].Text
			i += 3
		case sqltext.Word(ts[i], "COLLATE"):
			i++
			if i >= len(ts) {
				return f, errors.New("collation is missing")
			}
			f.Collation = ts[i].Text
			i++
		case sqltext.Word(ts[i], "COMMENT"):
			i++
			if i >= len(ts) || !strings.HasPrefix(ts[i].Text, "'") {
				return f, errors.New("comment must be a string literal")
			}
			f.Comment = decodeLiteral(ts[i].Text)
			i++
		case sqltext.Word(ts[i], "AUTO_INCREMENT"):
			f.AutoIncrement = true
			i++
		case sqltext.Word(ts[i], "ON") && i+2 < len(ts) && sqltext.Word(ts[i+1], "UPDATE"):
			i += 3
			if i < len(ts) && ts[i].Text == "(" {
				i, e = balanced(ts, i)
				if e != nil {
					return f, e
				}
			}
			extras = append(extras, raw[ts[start].Start:ts[i-1].End])
		case sqltext.Word(ts[i], "INVISIBLE") || sqltext.Word(ts[i], "VISIBLE"):
			i++
			extras = append(extras, ts[start].Text)
		default:
			f.Unsupported = true
			extras = append(extras, raw[ts[i].Start:])
			i = len(ts)
		}
	}
	f.Extras = strings.Join(extras, " ")
	return f, nil
}
func balanced(ts []sqltext.Token, start int) (int, error) {
	depth := 0
	for i := start; i < len(ts); i++ {
		if ts[i].Quoted || ts[i].Comment {
			continue
		}
		if ts[i].Text == "(" {
			depth++
		}
		if ts[i].Text == ")" {
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, errors.New("unbalanced SQL expression")
}
func decodeLiteral(s string) string {
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' && i+1 < len(s) && s[i+1] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case '0':
				b.WriteByte(0)
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			case 'Z':
				b.WriteByte(26)
			case '_', '%':
				b.WriteByte('\\')
				b.WriteByte(s[i])
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
func Literal(value string) string {
	// Backslash escapes are MySQL's SHOW CREATE default. The executor rejects
	// incompatible SQL modes before executing a preview generated this way.
	r := strings.NewReplacer("\\", "\\\\", "'", "''", "\x00", "\\0", "\n", "\\n", "\r", "\\r")
	return "'" + r.Replace(value) + "'"
}
func (f Field) SQL() (string, error) {
	if f.Unsupported {
		return "", errors.New("unsupported field attributes must remain unchanged")
	}
	name, e := sqltext.Identifier(f.Name, true)
	if e != nil {
		return "", e
	}
	if strings.TrimSpace(f.Type) == "" {
		return "", errors.New("field type is required")
	}
	parts := []string{name, f.Type}
	if f.Charset != "" {
		parts = append(parts, "CHARACTER SET "+f.Charset)
	}
	if f.Collation != "" {
		parts = append(parts, "COLLATE "+f.Collation)
	}
	if f.Generation != "" {
		if f.DefaultKind != NoDefault || f.AutoIncrement {
			return "", errors.New("generated fields cannot have defaults or auto increment")
		}
		storage := f.Storage
		if storage == "" {
			storage = "VIRTUAL"
		}
		if storage != "VIRTUAL" && storage != "STORED" {
			return "", errors.New("unsupported generated storage")
		}
		parts = append(parts, "GENERATED ALWAYS AS ("+f.Generation+") "+storage)
	}
	if !f.Nullable {
		parts = append(parts, "NOT NULL")
	}
	switch f.DefaultKind {
	case NoDefault:
	case NullDefault:
		if !f.Nullable {
			return "", errors.New("non-null field cannot default to NULL")
		}
		parts = append(parts, "DEFAULT NULL")
	case LiteralDefault:
		parts = append(parts, "DEFAULT "+Literal(f.Default))
	case ExpressionDefault:
		if strings.TrimSpace(f.Default) == "" {
			return "", errors.New("default expression is required")
		}
		parts = append(parts, "DEFAULT "+f.Default)
	default:
		return "", errors.New("unsupported default mode")
	}
	if f.AutoIncrement {
		parts = append(parts, "AUTO_INCREMENT")
	}
	if f.Comment != "" {
		parts = append(parts, "COMMENT "+Literal(f.Comment))
	}
	if f.Extras != "" {
		parts = append(parts, f.Extras)
	}
	raw := strings.Join(parts, " ")
	if e = singleFragment(raw); e != nil {
		return "", e
	}
	parsed, e := ParseColumn(raw)
	if e != nil || parsed.Unsupported {
		return "", errors.New("field properties form an unsupported SQL definition")
	}
	return raw, nil
}
