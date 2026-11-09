package sqlworkspace

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
)

type streamLease interface {
	ExecuteStream(context.Context, string, string, sqlresult.Sink) (contract.SQLResult, error)
}

func (s *State) RunExport(d *Document) {
	if d == nil || d.loading || d.recoveryNeeded || !s.connected(d.profileID) {
		return
	}
	lease, ok := d.lease.(streamLease)
	if !ok {
		d.err = "当前会话不支持流式导出"
		return
	}
	var graphExecute func(context.Context, string, string) (contract.SQLResult, error)
	if d.cypher() {
		var e error
		graphExecute, e = s.graphExecutor(d)
		if e != nil {
			d.err = e.Error()
			return
		}
	}
	f := s.persistence.files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	statement := strings.TrimSpace(d.Editor.SelectedText())
	if statement == "" {
		statement = s.statementAtCaret(d)
	}
	if statement == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.request++
	request, id, db, name := d.request, d.profileID, d.database, d.Name()+".zip"
	ch := make(chan executionResult, 1)
	d.results = ch
	d.cancel = cancel
	d.loading = true
	d.cancelRequested = false
	d.result = nil
	invalidate := s.Invalidate
	history := s.beginHistory(d, "export", statement)
	go func() {
		path, e := f.SavePath(name)
		var result contract.SQLResult
		executed := false
		if e == nil && path != "" {
			e = f.WriteStream(path, func(w io.Writer) error {
				archive := zip.NewWriter(w)
				var encoder *json.Encoder
				type manifestSet struct {
					File         string
					Rows         int
					AffectedRows int64
				}
				var manifest []manifestSet
				sink := func(i int, columns []contract.Column, row []contract.Cell) error {
					if e := ctx.Err(); e != nil {
						return e
					}
					if row == nil {
						entry := fmt.Sprintf("result-%03d.jsonl", i+1)
						v, e := archive.Create(entry)
						if e != nil {
							return e
						}
						encoder = json.NewEncoder(v)
						manifest = append(manifest, manifestSet{File: entry})
						return encoder.Encode(struct {
							Columns []contract.Column `json:"columns"`
						}{columns})
					}
					manifest[i].Rows++
					return encoder.Encode(row)
				}
				var runErr error
				executed = true
				if graphExecute != nil {
					result, runErr = graphExecute(ctx, db, statement)
					if runErr == nil {
						for i, set := range result.Sets {
							if runErr = sink(i, set.Columns, nil); runErr != nil {
								break
							}
							for _, row := range set.Rows {
								if runErr = sink(i, set.Columns, row); runErr != nil {
									break
								}
							}
							if runErr != nil {
								break
							}
						}
					}
					if runErr == nil && result.Graph != nil {
						var entry io.Writer
						entry, runErr = archive.Create("graph.json")
						if runErr == nil {
							runErr = json.NewEncoder(entry).Encode(result.Graph)
						}
					}
				} else {
					result, runErr = lease.ExecuteStream(ctx, db, statement, sink)
				}
				if runErr != nil {
					_ = archive.Close()
					return runErr
				}
				for i, set := range result.Sets {
					if i < len(manifest) {
						manifest[i].AffectedRows = set.AffectedRows
					}
				}
				v, e := archive.Create("manifest.json")
				if e != nil {
					return e
				}
				if e = json.NewEncoder(v).Encode(struct {
					Version int
					Sets    []manifestSet
				}{1, manifest}); e != nil {
					return e
				}
				if e = archive.Close(); e != nil {
					return e
				}
				return ctx.Err()
			})
		}
		if e == nil && path == "" {
			e = context.Canceled
		}
		completeHistory(history, result, e, executed)
		ch <- executionResult{request: request, profileID: id, data: result, err: e}
		if invalidate != nil {
			invalidate()
		}
	}()
}
