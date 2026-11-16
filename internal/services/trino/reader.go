package trino

import (
	"context"
	"encoding/json"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// rowReader preserves each transport's typed cells, including incomplete JDBC
// projections. Shared metadata/grid algorithms never decode one wire as another.
type rowReader interface {
	Read(context.Context, string, func([]contract.Column, []contract.Cell) error) error
	Close() error
}

func (c *Client) Read(ctx context.Context, source string, consume func([]contract.Column, []contract.Cell) error) error {
	_, err := c.Run(ctx, source, func(cols []Column, row []json.RawMessage) error {
		cells := make([]contract.Cell, len(row))
		for i, raw := range row {
			value, err := nativeCell(raw, cols[i].Type)
			if err != nil {
				return err
			}
			cells[i] = value
		}
		return consume(nativeColumns(cols), cells)
	})
	return err
}

func (s *Session) reader(ctx context.Context, metadata bool) (rowReader, error) {
	if s.managed != nil {
		return s.managed.reader(ctx, metadata)
	}
	return s.open(ctx)
}
