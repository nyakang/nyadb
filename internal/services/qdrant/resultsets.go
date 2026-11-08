package qdrant

import (
	"bytes"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type pointResponse uint8

const (
	noPointResponse pointResponse = iota
	pointObjectResponse
	pointListResponse
	pointSingleResponse
	pointCountResponse
)

// Only these exact native routes certify POST reads and point envelopes.
// The configured service prefix is applied by Client after classification.
func pointResponseRoute(r Request) pointResponse {
	u, err := url.Parse(r.Path)
	if err != nil {
		return noPointResponse
	}
	p := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	for i, part := range p {
		decoded, err := url.PathUnescape(part)
		if err != nil || strings.Contains(decoded, "/") {
			return noPointResponse
		}
		p[i] = decoded
	}
	if len(p) < 3 || p[0] != "collections" || p[2] != "points" {
		return noPointResponse
	}
	if _, err := collectionPath(p[1]); err != nil {
		return noPointResponse
	}
	if r.Method == "POST" {
		if len(p) == 3 {
			return pointListResponse // retrieve IDs
		}
		if len(p) == 4 {
			switch p[3] {
			case "query", "scroll":
				return pointObjectResponse
			case "search", "recommend", "discover":
				return pointListResponse
			case "count":
				return pointCountResponse
			}
		}
	}
	if r.Method == "GET" && len(p) == 4 {
		raw, _ := json.Marshal(p[3])
		if validPointID([]byte(p[3])) || validPointID(raw) {
			return pointSingleResponse
		}
	}
	return noPointResponse
}

func queryReadOnly(r Request) bool { return readOnly(r) || pointResponseRoute(r) != noPointResponse }

func validCount(raw json.RawMessage) bool {
	n, err := strconv.ParseUint(string(raw), 10, 64)
	return err == nil && strconv.FormatUint(n, 10) == string(raw)
}

const pointResultLimit = 10000
const pointColumnLimit = 1024
const pointCellLimit = 1 << 20

func nativeCell(raw json.RawMessage) contract.Cell {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
		return contract.Cell{Kind: contract.Null}
	case raw[0] == '"':
		var s string
		_ = json.Unmarshal(raw, &s) // whole native envelope has already been validated
		return contract.Cell{Kind: contract.Text, Text: s}
	case bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false")):
		return contract.Cell{Kind: contract.Boolean, Text: string(raw)}
	case raw[0] == '{' || raw[0] == '[':
		return contract.Cell{Kind: contract.JSON, Text: string(raw)}
	case bytes.ContainsAny(raw, ".eE"):
		return contract.Cell{Kind: contract.Decimal, Text: string(raw)}
	default:
		return contract.Cell{Kind: contract.Integer, Text: string(raw)}
	}
}

// A collision anywhere in this result keeps every payload wrapper intact.
// Business fields cannot share a column with a native score/vector/ID from
// another point. Nested payload values stay exact JSON, with no recursive flatten.
func pointResultSet(points []Point) (contract.ResultSet, error) {
	rows := make([]map[string]json.RawMessage, len(points))
	rootNames := map[string]bool{"id": true}
	for i, point := range points {
		row, err := object(point.JSON)
		if err != nil {
			return contract.ResultSet{}, err
		}
		rows[i] = row
		for key := range row {
			rootNames[key] = true
		}
	}
	flatten := true
	for _, row := range rows {
		if payload, err := object(row["payload"]); err == nil {
			for key := range payload {
				if rootNames[key] {
					flatten = false
				}
			}
		}
	}
	names := map[string]bool{"id": true}
	for _, row := range rows {
		if payload, err := object(row["payload"]); flatten && err == nil {
			delete(row, "payload")
			for key, raw := range payload {
				row[key] = raw
			}
		}
		for key := range row {
			names[key] = true
		}
		if len(names) > pointColumnLimit {
			return contract.ResultSet{}, ErrBounds
		}
	}
	if len(names)*len(rows) > pointCellLimit {
		return contract.ResultSet{}, ErrBounds
	}
	order := make([]string, 0, len(names))
	for _, name := range []string{"id", "score", "version", "shard_key", "vector"} {
		if names[name] {
			order = append(order, name)
			delete(names, name)
		}
	}
	rest := make([]string, 0, len(names))
	for name := range names {
		rest = append(rest, name)
	}
	sort.Strings(rest)
	order = append(order, rest...)
	set := contract.ResultSet{Columns: make([]contract.Column, len(order)), Rows: make([][]contract.Cell, len(rows))}
	for j, name := range order {
		set.Columns[j].Name = name
	}
	for i, row := range rows {
		set.Rows[i] = make([]contract.Cell, len(order))
		for j, name := range order {
			cell := nativeCell(row[name])
			set.Rows[i][j] = cell
			col := &set.Columns[j]
			if cell.Kind == contract.Null {
				col.Nullable = true
			} else if col.DatabaseType == "" {
				col.DatabaseType = string(cell.Kind)
			} else if col.DatabaseType != string(cell.Kind) {
				col.DatabaseType = "mixed"
			}
		}
	}
	for i := range set.Columns {
		if set.Columns[i].DatabaseType == "" {
			set.Columns[i].DatabaseType = "native JSON"
		}
	}
	return set, nil
}

// raw HTTP evidence remains a separate final result set, including cursors,
// timing and all unknown fields. A projection failure never discards that body.
func typedPointResult(r Request, response Response) (*contract.ResultSet, error) {
	origin := pointResponseRoute(r)
	if origin == noPointResponse {
		return nil, nil
	}
	raw, err := NativeResult(response.Body)
	if err != nil {
		return nil, err
	}
	if origin == pointCountResponse {
		fields, err := object(raw)
		if err != nil || !validCount(fields["count"]) {
			return nil, ErrProtocol
		}
		return &contract.ResultSet{Columns: []contract.Column{{Name: "count", DatabaseType: "integer"}}, Rows: [][]contract.Cell{{nativeCell(fields["count"])}}}, nil
	}
	if origin == pointObjectResponse {
		fields, err := object(raw)
		if err != nil {
			return nil, ErrProtocol
		}
		raw = fields["points"]
	}
	if origin == pointSingleResponse {
		raw = append(append([]byte{'['}, raw...), ']')
	}
	var rows []json.RawMessage
	if decodeJSON(raw, &rows) != nil || rows == nil {
		return nil, ErrProtocol
	}
	if len(rows) > pointResultLimit {
		return nil, ErrBounds
	}
	points, err := parsePoints(raw, pointResultLimit)
	if err != nil {
		return nil, err
	}
	set, err := pointResultSet(points)
	return &set, err
}
