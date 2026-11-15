package h2

import (
	"context"
	"encoding/json"
	"io"
	"math"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataExports() contract.DataExports {
	return contract.DataExports{Exporter: s.export, JSONL: s.jsonl}
}

func (s *Session) export(ctx context.Context, id int64, schema, name string, options contract.DataOptions, names []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return connections.ErrInvalid
	}
	var sinkErr error
	err := s.withFeature(ctx, func(ctx context.Context, session *agentbridge.Session) (err error) {
		t, err := s.resolve(ctx, session, id, schema, name, options.Object)
		if err != nil {
			return err
		}
		var columns []contract.StructureColumn
		if len(names) == 0 {
			columns = append(columns, t.Columns...)
		} else {
			seen := map[string]bool{}
			for _, name := range names {
				if seen[name] {
					return connections.ErrInvalid
				}
				seen[name] = true
				found := false
				for _, c := range t.Columns {
					if name == c.Name {
						columns, found = append(columns, c), true
					}
				}
				if !found {
					return connections.ErrInvalid
				}
			}
		}
		query, err := readQuery(t, options, false, columns, contract.DataExportFieldMaxBytes, true)
		if err != nil {
			return err
		}
		// Keep one original native result set for the whole export. Fetching
		// never reissues SQL or switches to another profile/session.
		cursor, page, err := session.OpenQueryCursor(ctx, agentbridge.QueryCursorOptions{SQL: query, Schema: schema, PageSize: 1, MaxRows: math.MaxInt32, TimeoutSeconds: s.profile.H2Options.QueryTimeoutSeconds, Decoder: decodeValue})
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := cursor.Close(); err == nil {
				err = closeErr
			}
		}()
		projection := &contract.TableStructure{Columns: columns}
		for {
			if page.SQL.Truncated || len(page.SQL.Sets) != 1 {
				return contract.ErrDataEditLimit
			}
			for _, raw := range page.SQL.Sets[0].Rows {
				n := len(columns)
				if len(raw) != n*3+1 || raw[n*3].Text != "1" {
					return contract.ErrDataEditLimit
				}
				cells, _, _, err := rowCells(projection, raw[:n*3], contract.DataExportFieldMaxBytes, false)
				if err != nil {
					return err
				}
				values := make([]contract.DataValue, len(cells))
				for i, v := range cells {
					if v.Truncated {
						return contract.ErrDataEditLimit
					}
					values[i] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary}
				}
				if sinkErr = sink(values); sinkErr != nil {
					return sinkErr
				}
			}
			if !page.HasMore {
				return nil
			}
			page, err = cursor.Fetch(ctx, 1)
			if err != nil {
				return err
			}
		}
	})
	if sinkErr != nil {
		return sinkErr
	}
	return err
}

func (s *Session) jsonl(ctx context.Context, object contract.ObjectRef, writer io.Writer) error {
	if writer == nil {
		return connections.ErrInvalid
	}
	encoder := json.NewEncoder(writer)
	return s.export(ctx, object.ProfileID, object.Schema, object.Name, contract.DataOptions{Object: object}, nil, func(values []contract.DataValue) error { return encoder.Encode(values) })
}
