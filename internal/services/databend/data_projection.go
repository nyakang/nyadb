package databend

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const dataPreviewUnits = 1024
const dataReadAlias = "__nyadb_original_data"

// The native bridge returns a whole row per frame (64 MiB, including JSON
// escaping). Exports stream arbitrary row counts with a separate 8 MiB row
// budget; complete editing retains the shared 1 MiB limit.
const dataExportMaxBytes = 8 << 20

// Binary digests cover the original native hex representation, not invented
// raw-byte SHA256 support. They stay in a signed private grant; snapshots use
// typed values and never publish these as generic fingerprint payloads.
func dataText(c contract.StructureColumn, column string) (string, error) {
	t := strings.ToUpper(strings.TrimSpace(c.Type))
	for strings.HasPrefix(t, "NULLABLE(") && strings.HasSuffix(t, ")") {
		t = strings.TrimSpace(t[len("NULLABLE(") : len(t)-1])
	}
	switch c.ValueKind {
	case contract.Binary:
		return "hex(" + column + ")", nil
	case contract.JSON:
		if t == "VARIANT" {
			return "json_pretty(" + column + ")", nil
		}
		return "json_pretty(to_variant(" + column + "))", nil
	case contract.Text:
		if strings.HasPrefix(t, "TUPLE(") {
			return "json_pretty(to_variant(" + column + "))", nil
		}
		if t != "VARCHAR" && t != "STRING" && t != "TEXT" {
			return "", contract.ErrUnsupported
		}
		return column, nil
	case contract.Integer, contract.Decimal, contract.Floating, contract.Boolean, contract.DateTime:
		return "to_string(" + column + ")", nil
	default:
		return "", contract.ErrUnsupported
	}
}

func dataUnits(c contract.StructureColumn, text string) string {
	if c.ValueKind == contract.Binary {
		return "CAST(octet_length(" + text + ")/2 AS UInt64)"
	}
	return "char_length(" + text + ")"
}

type dataProjection struct {
	columns  []contract.StructureColumn
	selects  []string
	rowID    bool
	complete bool
	rawSlice bool
	offset   int64
	limit    int
}

func newDataProjection(m *contract.TableStructure, fields []string, limit int, offset int64, rowID, complete bool, totalColumn string) (*dataProjection, error) {
	if m == nil || limit < 1 || limit > dataExportMaxBytes || offset < 0 || offset > math.MaxInt64-int64(limit) {
		return nil, contract.ErrDataPage
	}
	known := map[string]contract.StructureColumn{}
	for _, c := range m.Columns {
		known[c.Name] = c
	}
	if len(fields) == 0 {
		for _, c := range m.Columns {
			fields = append(fields, c.Name)
		}
	}
	if len(fields) == 0 || len(fields) > 512 {
		return nil, contract.ErrDataPage
	}
	p := &dataProjection{rowID: rowID, complete: complete, offset: offset, limit: limit}
	seen := map[string]bool{}
	for i, name := range fields {
		c, ok := known[name]
		if !ok || seen[name] {
			return nil, contract.ErrDataPage
		}
		seen[name] = true
		column := quoteName(dataReadAlias) + "." + quoteName(name)
		text, err := dataText(c, column)
		if err != nil {
			return nil, err
		}
		start, size := offset+1, int64(limit)
		if c.ValueKind == contract.Binary {
			if offset > (math.MaxInt64-1)/2 {
				return nil, contract.ErrDataPage
			}
			start, size = offset*2+1, int64(limit)*2
		}
		value := fmt.Sprintf("substr(%s,%d,%d)", text, start, size)
		if complete {
			if totalColumn == "" {
				return nil, contract.ErrDataPage
			}
			value = fmt.Sprintf("IF(%s.%s<=%d,%s,NULL)", quoteName(dataReadAlias), quoteName(totalColumn), limit, text)
		}
		p.columns = append(p.columns, c)
		p.selects = append(p.selects, value+" AS "+quoteName(fmt.Sprintf("__nyadb_value_%d", i)), dataUnits(c, text)+" AS "+quoteName(fmt.Sprintf("__nyadb_units_%d", i)), "sha2("+text+",256) AS "+quoteName(fmt.Sprintf("__nyadb_digest_%d", i)))
	}
	if rowID {
		p.selects = append(p.selects, quoteName(dataReadAlias)+`."_row_id" AS "__nyadb_row_id"`)
	}
	return p, nil
}

func (p *dataProjection) decode(columns []contract.Column, row []contract.Cell) ([]contract.Cell, []*string, []int64, string, error) {
	n := 3 * len(p.columns)
	if p.rowID {
		n++
	}
	if len(columns) != n || len(row) != n {
		return nil, nil, nil, "", contract.ErrDataPage
	}
	var cells []contract.Cell
	var digests []*string
	var units []int64
	budget := 0
	for i, c := range p.columns {
		for j, label := range []string{"value", "units", "digest"} {
			if columns[i*3+j].Name != fmt.Sprintf("__nyadb_%s_%d", label, i) {
				return nil, nil, nil, "", contract.ErrDataPage
			}
		}
		value, length, digest := row[i*3], row[i*3+1], row[i*3+2]
		if length.Kind == contract.Null {
			if value.Kind != contract.Null || digest.Kind != contract.Null {
				return nil, nil, nil, "", contract.ErrDataPage
			}
			cells = append(cells, contract.Cell{Kind: contract.Null})
			digests = append(digests, nil)
			units = append(units, 0)
			continue
		}
		u, err := strconv.ParseInt(length.Text, 10, 64)
		if err != nil || length.Kind != contract.Integer || u < 0 || digest.Kind != contract.Text || len(digest.Text) != 64 || strings.ToLower(digest.Text) != digest.Text {
			return nil, nil, nil, "", contract.ErrDataPage
		}
		if _, err = hex.DecodeString(digest.Text); err != nil {
			return nil, nil, nil, "", contract.ErrDataPage
		}
		if p.complete && value.Kind == contract.Null {
			return nil, nil, nil, "", p.limitError()
		}
		if value.Kind != contract.Text || value.Truncated || !utf8.ValidString(value.Text) {
			return nil, nil, nil, "", contract.ErrDataPage
		}
		cell := contract.Cell{Kind: c.ValueKind, Text: value.Text, Truncated: !p.complete && u > p.offset+int64(p.limit)}
		if p.rawSlice && c.ValueKind != contract.Binary {
			cell.Kind = contract.Text
		}
		switch cell.Kind {
		case contract.Binary:
			decoded, err := hex.DecodeString(value.Text)
			if err != nil {
				return nil, nil, nil, "", contract.ErrDataPage
			}
			cell.Text, cell.Bytes = "", decoded
		case contract.JSON:
			if !cell.Truncated {
				var compact bytes.Buffer
				if json.Compact(&compact, []byte(cell.Text)) != nil {
					return nil, nil, nil, "", contract.ErrDataPage
				}
				cell.Text = compact.String()
			}
		case contract.Integer:
			if !cell.Truncated {
				if strings.Contains(strings.ToUpper(c.Type), "UNSIGNED") || strings.Contains(strings.ToUpper(c.Type), "UINT") {
					_, err = strconv.ParseUint(cell.Text, 10, 64)
				} else {
					_, err = strconv.ParseInt(cell.Text, 10, 64)
				}
				if err != nil {
					return nil, nil, nil, "", contract.ErrDataPage
				}
			}
		case contract.Boolean:
			if cell.Text != "true" && cell.Text != "false" {
				return nil, nil, nil, "", contract.ErrDataPage
			}
		case contract.Floating:
			if _, err = strconv.ParseFloat(cell.Text, 64); err != nil {
				return nil, nil, nil, "", contract.ErrDataPage
			}
		}
		budget += len(cell.Text) + len(cell.Bytes)
		if p.complete && budget > p.limit {
			return nil, nil, nil, "", p.limitError()
		}
		v := digest.Text
		cells = append(cells, cell)
		digests = append(digests, &v)
		units = append(units, u)
	}
	id := ""
	if p.rowID {
		last := row[len(row)-1]
		if columns[len(columns)-1].Name != "__nyadb_row_id" || last.Kind != contract.Integer || last.Truncated {
			return nil, nil, nil, "", contract.ErrDataPage
		}
		if _, err := strconv.ParseUint(last.Text, 10, 64); err != nil {
			return nil, nil, nil, "", contract.ErrDataPage
		}
		id = last.Text
	}
	return cells, digests, units, id, nil
}

func (p *dataProjection) limitError() error {
	if p.limit == contract.DataEditMaxBytes {
		return contract.ErrDataEditLimit
	}
	return contract.ErrDataPage
}
