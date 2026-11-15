package rqlite

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Dynamic SQLite storage identity is authoritative, irrespective of affinity.
// REAL uses SQLite's extended precision representation rather than its lossy
// default CAST text or JSON's integral-real tokens.
func dataBytes(value string) string {
	return "CASE WHEN typeof(" + value + ")='real' THEN CAST(printf('%!.26g'," + value + ") AS BLOB) ELSE CAST(" + value + " AS BLOB) END"
}

// source yields the original ordered page, with internal c0..cN aliases. The
// full values remain inside the original SQLite statement. Only bounded hex
// previews, original byte lengths, storage types and complete digests leave it.
func dataProjection(source string, columns, limit int, full bool) string {
	ordinals := make([]string, columns)
	cases := make([]string, columns)
	for i := range columns {
		ordinals[i] = fmt.Sprintf("(%d)", i)
		cases[i] = fmt.Sprintf("WHEN %d THEN r.c%d", i, i)
	}
	value := "CASE c.i " + strings.Join(cases, " ") + " END"
	input := "SELECT id,CASE WHEN typ='null' THEN NULL ELSE CAST(CAST(typ||char(0) AS BLOB)||v AS BLOB) END FROM _nyadb_data_values"
	projection := fmt.Sprintf("hex(substr(v,1,%d))", limit)
	if full {
		// The total bound and the complete read happen in the same statement.
		projection = "CASE WHEN sum(coalesce(length(v),0)) OVER (PARTITION BY rid)<=" + strconv.Itoa(contract.DataEditMaxBytes) + " THEN " + projection + " ELSE NULL END"
		input = "SELECT id,CASE WHEN sum(coalesce(length(v),0)) OVER (PARTITION BY rid)>" + strconv.Itoa(contract.DataEditMaxBytes) + " OR typ='null' THEN NULL ELSE CAST(CAST(typ||char(0) AS BLOB)||v AS BLOB) END FROM _nyadb_data_values"
	}
	return "WITH RECURSIVE _nyadb_data_raw AS MATERIALIZED (SELECT row_number() OVER ()-1 AS rid,* FROM (" + source + "))," +
		"_nyadb_data_columns(i) AS (VALUES " + strings.Join(ordinals, ",") + ")," +
		fmt.Sprintf("_nyadb_data_cells(id,rid,cid,typ,value) AS MATERIALIZED (SELECT r.rid*%d+c.i,r.rid,c.i,typeof(%s),%s FROM _nyadb_data_raw r CROSS JOIN _nyadb_data_columns c),", columns, value, value) +
		"_nyadb_data_values(id,rid,cid,typ,v) AS MATERIALIZED (SELECT id,rid,cid,typ," + dataBytes("value") + " FROM _nyadb_data_cells)," +
		hashCTEs(input) + ",_nyadb_data_preview AS MATERIALIZED (SELECT id,rid,cid,typ," + projection + " AS preview,length(v) AS bytes FROM _nyadb_data_values)" +
		" SELECT p.rid,p.cid,p.typ,p.preview,p.bytes,h.digest FROM _nyadb_data_preview p JOIN _nyadb_hash h ON h.id=p.id ORDER BY p.rid,p.cid"
}

type dataRow struct {
	cells []contract.Cell
	fp    []*string
}

func decodeDataRows(r receipt, columns, limit, maxRows int, full bool) ([]dataRow, error) {
	raw, err := decodedRows(r, 0, 6)
	if err != nil || len(raw) > columns*maxRows || len(raw)%columns != 0 {
		return nil, errIncomplete
	}
	rows := make([]dataRow, len(raw)/columns)
	for i := range rows {
		rows[i] = dataRow{cells: make([]contract.Cell, columns), fp: make([]*string, columns)}
	}
	total := 0
	budget := 16 << 20
	for i, fields := range raw {
		rid, e := cellInt(fields[0])
		cid, e2 := cellInt(fields[1])
		if e != nil || e2 != nil || rid != int64(i/columns) || cid != int64(i%columns) {
			return nil, errIncomplete
		}
		typ, e := cellText(fields[2])
		if e != nil {
			return nil, errIncomplete
		}
		row := &rows[rid]
		if typ == "null" {
			// hex(NULL) is an empty string; distinguish it using typeof/length.
			if fields[4].Kind != contract.Null || fields[5].Kind != contract.Null || fields[3].Kind != contract.Null && fields[3].Text != "" {
				return nil, errIncomplete
			}
			row.cells[cid].Kind = contract.Null
			continue
		}
		length, e := cellInt(fields[4])
		if e != nil || length < 0 {
			return nil, errIncomplete
		}
		if full && (length > contract.DataEditMaxBytes || fields[3].Kind == contract.Null) {
			return nil, contract.ErrDataEditLimit
		}
		preview, e := cellText(fields[3])
		if e != nil || len(preview) > limit*2 {
			return nil, errIncomplete
		}
		data, e := hex.DecodeString(preview)
		if e != nil || int64(len(data)) != min(length, int64(limit)) {
			return nil, errIncomplete
		}
		fingerprint, e := cellText(fields[5])
		if e != nil || len(fingerprint) != 64 {
			return nil, errIncomplete
		}
		if _, e := hex.DecodeString(fingerprint); e != nil {
			return nil, errIncomplete
		}
		row.fp[cid] = &fingerprint
		v := contract.Cell{Truncated: int64(len(data)) < length}
		switch typ {
		case "blob":
			v.Kind, v.Bytes = contract.Binary, bytes.Clone(data)
		case "text":
			// Trim only a partial trailing code point. Invalid original UTF-8
			// inside the range is explicitly unsupported, never normalized.
			if v.Truncated && len(data) > 0 {
				start := len(data) - 1
				for start > 0 && !utf8.RuneStart(data[start]) {
					start--
				}
				if !utf8.FullRune(data[start:]) {
					data = data[:start]
				}
			}
			if !utf8.Valid(data) {
				return nil, contract.ErrUnsupported
			}
			v.Kind, v.Text = contract.Text, string(data)
		case "integer":
			if v.Truncated {
				return nil, errIncomplete
			}
			if _, e := strconv.ParseInt(string(data), 10, 64); e != nil {
				return nil, errIncomplete
			}
			v.Kind, v.Text = contract.Integer, string(data)
		case "real":
			if v.Truncated {
				return nil, errIncomplete
			}
			if _, e := strconv.ParseFloat(string(data), 64); e != nil {
				return nil, contract.ErrUnsupported
			}
			v.Kind, v.Text = contract.Floating, string(data)
		default:
			return nil, errIncomplete
		}
		if full && v.Truncated {
			return nil, contract.ErrDataEditLimit
		}
		row.cells[cid] = v
		total += len(v.Text) + len(v.Bytes)
		budget -= len(v.Text) + len(v.Bytes) + 128
		if budget < 0 || full && total > contract.DataEditMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
	}
	return rows, nil
}
