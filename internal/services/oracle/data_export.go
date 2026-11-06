package oracle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataExports() contract.DataExports {
	return contract.DataExports{Exporter: s.export, JSONL: s.jsonl}
}
func (s *Session) export(ctx context.Context, id int64, schema, name string, o contract.DataOptions, columns []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return connections.ErrInvalid
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	t, err := s.resolve(child, s.db, id, schema, name, o.Object)
	if err != nil {
		return err
	}
	query, args, err := dataQuery(t, o, columns, false)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		for _, col := range t.Columns {
			columns = append(columns, col.Name)
		}
	}
	query, err = boundedExportQuery(t, query, columns)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(child, query, args...)
	if err != nil {
		return safeError(err)
	}
	defer rows.Close()
	budget := 0
	_, err = scanRows(rows, 0, streamSink(func(_ int, _ []contract.Column, cells []contract.Cell) error {
		if len(cells) != len(columns)+1 || cells[len(columns)].Text != "1" {
			return contract.ErrDataEditLimit
		}
		cells = cells[:len(columns)]
		values := make([]contract.DataValue, len(cells))
		size := 0
		for i, c := range cells {
			if len(c.Text)+len(c.Bytes) > contract.DataExportFieldMaxBytes {
				return contract.ErrDataEditLimit
			}
			size += len(c.Text) + len(c.Bytes)
			values[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: c.Bytes, Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
		}
		if size > contract.DataExportRowMaxBytes {
			return contract.ErrDataEditLimit
		}
		return sink(values)
	}), &budget, 0, contract.DataExportFieldMaxBytes)
	return safeError(err)
}

// Guard on the server before materializing large values. The final marker
// lets the exporter reject oversized rows instead of silently omitting them.
func boundedExportQuery(t *contract.TableStructure, query string, names []string) (string, error) {
	marker := "nyadb_export_complete"
	for {
		collision := false
		for _, col := range t.Columns {
			if strings.EqualFold(col.Name, marker) {
				collision = true
				break
			}
		}
		if !collision {
			break
		}
		marker += "_"
	}
	var lengths, conditions, fields []string
	for _, name := range names {
		for _, col := range t.Columns {
			if col.Name == name {
				length := "(" + valueLength(col) + ")"
				if col.ValueKind == contract.Text {
					length += "*4"
				}
				lengths = append(lengths, length)
				conditions = append(conditions, fmt.Sprintf("%s<=%d", length, contract.DataExportFieldMaxBytes))
			}
		}
	}
	conditions = append(conditions, fmt.Sprintf("(%s)<=%d", strings.Join(lengths, "+"), contract.DataExportRowMaxBytes))
	valid := strings.Join(conditions, " AND ")
	for _, name := range names {
		fields = append(fields, "CASE WHEN "+valid+" THEN "+quote(name)+" ELSE NULL END AS "+quote(name))
	}
	fields = append(fields, "CASE WHEN "+valid+" THEN 1 ELSE 0 END AS "+quote(marker))
	tokens, err := sqltext.OracleTokens(query)
	if err != nil {
		return "", err
	}
	for _, token := range tokens {
		if sqltext.Word(token, "FROM") {
			prefix := "FROM " + qualified(t.Object)
			if !strings.HasPrefix(query[token.Start:], prefix) {
				return "", connections.ErrInvalid
			}
			return "SELECT " + strings.Join(fields, ",") + " " + query[token.Start:], nil
		}
	}
	return "", connections.ErrInvalid
}
func (s *Session) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if w == nil {
		return connections.ErrInvalid
	}
	enc := json.NewEncoder(w)
	return s.export(ctx, o.ProfileID, o.Schema, o.Name, contract.DataOptions{Object: o}, nil, func(values []contract.DataValue) error { return enc.Encode(values) })
}
