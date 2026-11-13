package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func encodeHTTPWatch(scope contract.KVScope, authored contract.KVRequest, snapshot contract.KVHTTPWatchSnapshot) ([]byte, error) {
	type scopeRecord struct {
		ProfileID  string                  `json:"profile_id"`
		Generation string                  `json:"generation"`
		Namespace  contract.NamespaceRef   `json:"namespace"`
		Prefix     []byte                  `json:"prefix"`
		Header     eventHeaderRecord       `json:"header"`
		Version    string                  `json:"version"`
		HTTP       contract.KVHTTPIdentity `json:"http"`
	}
	type observationRecord struct {
		Original           json.RawMessage `json:"original_http"`
		Matches            []int           `json:"matched_entry_ordinals"`
		ReceivedAt         string          `json:"local_received_at"`
		IndexRelation      string          `json:"index_relation"`
		ProjectionComplete bool            `json:"projection_complete"`
		IndexReset         bool            `json:"index_reset,omitempty"`
	}
	r := snapshot.Request
	record := struct {
		Version  int                `json:"version"`
		Scope    scopeRecord        `json:"original_scope"`
		Authored contract.KVRequest `json:"authored_request"`
		Request  struct {
			Target            string                            `json:"target"`
			Prefix            bool                              `json:"prefix"`
			ObservationPrefix string                            `json:"observation_prefix"`
			StartIndex        string                            `json:"start_index"`
			Wait              string                            `json:"wait"`
			MaxObservations   int                               `json:"max_observations"`
			MaxBytes          string                            `json:"max_bytes"`
			Health            *contract.KVHTTPHealthWatchTarget `json:"health,omitempty"`
		} `json:"captured_request"`
		Observations  []observationRecord          `json:"observations"`
		Ended         bool                         `json:"ended"`
		Overflow      bool                         `json:"terminal_overflow"`
		RetainedBytes string                       `json:"retained_bytes"`
		Terminal      contract.KVHTTPWatchTerminal `json:"terminal"`
	}{Version: 1, Scope: scopeRecord{strconv.FormatInt(scope.Namespace.ProfileID, 10), strconv.FormatUint(scope.Generation, 10), scope.Namespace, scope.Prefix, eventHeader(scope.Header), scope.Version, scope.HTTP}, Authored: authored, Ended: snapshot.Ended, Overflow: snapshot.TerminalOverflow,
		RetainedBytes: strconv.FormatInt(snapshot.RetainedBytes, 10), Terminal: snapshot.Terminal}
	record.Request.Target, record.Request.Prefix, record.Request.ObservationPrefix = r.Target, r.Prefix, r.ObservationPrefix
	record.Request.StartIndex, record.Request.Wait = strconv.FormatUint(r.StartIndex, 10), r.Wait.String()
	record.Request.MaxObservations, record.Request.MaxBytes = r.MaxObservations, strconv.FormatInt(r.MaxBytes, 10)
	if r.Health.Kind != "" {
		target := r.Health
		record.Request.Health = &target
	}
	for _, o := range snapshot.Observations {
		h := o.HTTP
		raw, err := encodeHTTPOperationReceipt(Receipt{Scope: scope, Authored: authored, Result: contract.KVResult{HTTP: &h}})
		if err != nil {
			return nil, err
		}
		record.Observations = append(record.Observations, observationRecord{Original: raw, Matches: o.Matches,
			ReceivedAt: o.ReceivedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), IndexRelation: o.IndexRelation, ProjectionComplete: o.ProjectionComplete, IndexReset: o.IndexReset})
	}
	return json.Marshal(record)
}

// Capture a complete immutable snapshot on the owning UI thread before a
// picker, clipboard job or disk worker can wait. This never consumes Poll.
func (s *State) HTTPWatchRecord(d *Document) ([]byte, error) {
	if !s.owned(d) || d.httpWatch == nil || d.httpWatch.run == nil {
		return nil, jsondocument.ErrInvalid
	}
	w := d.httpWatch
	return encodeHTTPWatch(w.evidenceScope.Clone(), w.authored, w.run.Snapshot())
}
func (s *State) ExportHTTPWatchRecord(d *Document) {
	if !s.active(d) || d.FileBusy() || d.httpWatch == nil || d.httpWatch.run == nil {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	w := d.httpWatch
	scope, authored, snapshot := w.evidenceScope.Clone(), w.authored, w.run.Snapshot()
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		raw, err := encodeHTTPWatch(scope, authored, snapshot)
		if err != nil {
			return fileResult{err: err}
		}
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath("http-watch-" + strconv.FormatUint(scope.Generation, 10) + ".nyakv-receipt.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
func (s *State) CopyHTTPWatchRecord(services ui.Services, d *Document) {
	if !s.active(d) || d.FileBusy() || d.httpWatch == nil || d.httpWatch.run == nil {
		return
	}
	w := d.httpWatch
	scope, authored, snapshot := w.evidenceScope.Clone(), w.authored, w.run.Snapshot()
	s.fileJob(d, "copy-http-watch", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		raw, err := encodeHTTPWatch(scope, authored, snapshot)
		if err == nil {
			err = ctx.Err()
		}
		return fileResult{clipboard: string(raw), err: err}
	})
	d.io.clipboard = services.WriteClipboard
}

func (d *Document) HTTPWatchStatus() contract.KVHTTPWatchStatus {
	if d.httpWatch == nil {
		return contract.KVHTTPWatchStatus{}
	}
	return d.httpWatch.status
}
