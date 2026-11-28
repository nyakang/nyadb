package manticore

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func writeLiteral(c contract.StructureColumn, v contract.DataValue) (string, error) {
	if v.Null || v.Default || v.Binary || len(v.Bytes) != 0 || v.Kind == contract.Null || !utf8.ValidString(v.Text) || strings.ContainsRune(v.Text, 0) {
		return "", connections.ErrInvalid
	}
	if len(v.Text) > contract.DataEditMaxBytes {
		return "", contract.ErrDataEditLimit
	}
	numeric := v.Kind == contract.Integer || v.Kind == contract.Decimal || v.Kind == contract.Floating || v.Kind == contract.Text || v.Kind == ""
	if c.Name == "id" && c.DataType == "bigint" && numeric && !strings.HasPrefix(v.Text, "-") {
		n, e := strconv.ParseUint(v.Text, 10, 64)
		if e != nil {
			return "", connections.ErrInvalid
		}
		return strconv.FormatUint(n, 10), nil
	}
	switch c.DataType {
	case "text", "string":
		if v.Kind != "" && v.Kind != contract.Text {
			return "", connections.ErrInvalid
		}
		return nativeLiteral(contract.StructureColumn{DataType: "string"}, v.Text)
	case "json":
		if v.Kind != "" && v.Kind != contract.JSON && v.Kind != contract.Text || !json.Valid([]byte(v.Text)) {
			return "", connections.ErrInvalid
		}
		return nativeLiteral(contract.StructureColumn{DataType: "string"}, v.Text)
	case "mva", "mva64":
		if v.Kind != "" && v.Kind != contract.Text {
			return "", connections.ErrInvalid
		}
		var parts []string
		if strings.TrimSpace(v.Text) != "" {
			for _, part := range strings.Split(v.Text, ",") {
				typ := "uint"
				if c.DataType == "mva64" {
					typ = "bigint"
				}
				literal, e := nativeLiteral(contract.StructureColumn{DataType: typ}, strings.TrimSpace(part))
				if e != nil {
					return "", e
				}
				parts = append(parts, literal)
			}
		}
		return "(" + strings.Join(parts, ",") + ")", nil
	case "bool":
		if v.Kind == contract.Boolean {
			switch v.Text {
			case "true", "1":
				return "1", nil
			case "false", "0":
				return "0", nil
			default:
				return "", connections.ErrInvalid
			}
		}
		if !numeric {
			return "", connections.ErrInvalid
		}
		return nativeLiteral(c, v.Text)
	case "float":
		if !numeric {
			return "", connections.ErrInvalid
		}
		n, e := strconv.ParseFloat(v.Text, 32)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return "", connections.ErrInvalid
		}
		return strconv.FormatFloat(n, 'g', -1, 32), nil
	case "bigint", "uint", "integer", "timestamp", "double":
		if !numeric {
			return "", connections.ErrInvalid
		}
		return nativeLiteral(c, v.Text)
	default:
		return "", contract.ErrUnsupported
	}
}
func writeKinds(m *contract.TableStructure) []string {
	switch m.Engine {
	case "rt":
		return []string{"insert", "update", "delete"}
	case "local":
		return []string{"update"}
	default:
		return []string{}
	}
}
func writePermissions(m *contract.TableStructure) map[string][]string {
	out := map[string][]string{}
	for _, c := range m.Columns {
		if m.Engine == "local" {
			// Plain uint attributes have verified conditional native updates.
			// Other attribute encodings require their own original acceptance.
			if c.Name != "id" && c.DataType == "uint" {
				out[c.Name] = []string{"update"}
			}
			continue
		}
		if m.Engine != "rt" {
			continue
		}
		switch c.DataType {
		case "text", "string", "json", "mva", "mva64", "bool", "float", "double", "bigint", "uint", "integer", "timestamp":
			out[c.Name] = []string{"insert"}
			if c.Name != "id" && c.DataType != "text" {
				out[c.Name] = append(out[c.Name], "update")
			}
		}
	}
	return out
}
