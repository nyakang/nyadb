package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"strings"
)

// Full export is explicit and atomic publication is injected by bootstrap.
func (s *EngineState) Export(d *EngineDocument) {
	if s.deps.SavePath == nil || s.deps.WriteStream == nil || d.Page == nil {
		return
	}
	o := d.Object
	pick, write := s.deps.SavePath, s.deps.WriteStream
	query := `SELECT * FROM "` + strings.ReplaceAll(o.Schema, `"`, `""`) + `"."` + strings.ReplaceAll(o.Name, `"`, `""`) + `"`
	s.run(d, "export", func(ctx context.Context, session contract.Session) engineResult {
		path, e := pick(o.Name + ".jsonl")
		if e != nil || path == "" {
			return engineResult{err: e}
		}
		lease, e := session.Reserve(ctx)
		if e != nil {
			return engineResult{err: e}
		}
		defer lease.Close()
		e = write(path, func(w io.Writer) error {
			sink := &engineJSONSink{ctx: ctx, encoder: json.NewEncoder(w)}
			_, e := lease.Stream(ctx, query, sink)
			if e != nil {
				return e
			}
			return ctx.Err()
		})
		return engineResult{err: e}
	})
}

type engineJSONSink struct {
	ctx     context.Context
	encoder *json.Encoder
}

func (s *engineJSONSink) BeginResult(i int, c []contract.Column) error {
	if i != 0 {
		return fmt.Errorf("table export expected one result")
	}
	return s.encoder.Encode(struct {
		Version int
		Columns []contract.Column
	}{1, c})
}
func (s *engineJSONSink) WriteRow(i int, row []contract.Cell) error {
	if e := s.ctx.Err(); e != nil {
		return e
	}
	return s.encoder.Encode(row)
}
func (s *engineJSONSink) EndResult(i int, count int64) error { return nil }
