package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func encodeHTTPSessionRenewal(scope contract.KVScope, authored contract.KVRequest, snapshot contract.KVHTTPSessionRenewalSnapshot) ([]byte, error) {
	type scopeRecord struct {
		ProfileID  string                  `json:"profile_id"`
		Generation string                  `json:"generation"`
		Namespace  contract.NamespaceRef   `json:"namespace"`
		Prefix     []byte                  `json:"prefix"`
		Header     eventHeaderRecord       `json:"header"`
		Version    string                  `json:"version"`
		HTTP       contract.KVHTTPIdentity `json:"http"`
	}
	type sessionRecord struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Node      string `json:"node"`
		TTL       string `json:"ttl"`
		Behavior  string `json:"behavior"`
		LockDelay string `json:"lock_delay"`
	}
	type observationRecord struct {
		Original           json.RawMessage `json:"original_http"`
		Sequence           int             `json:"local_sequence"`
		Session            *sessionRecord  `json:"native_session,omitempty"`
		ReceivedAt         string          `json:"local_received_at"`
		ProjectionError    string          `json:"projection_error"`
		ProjectionComplete bool            `json:"projection_complete"`
	}
	r := snapshot.Request
	record := struct {
		Version       int                                   `json:"version"`
		Scope         scopeRecord                           `json:"original_scope"`
		Authored      contract.KVRequest                    `json:"authored_request"`
		Request       HTTPSessionRenewalInput               `json:"captured_request"`
		Observations  []observationRecord                   `json:"observations"`
		Ended         bool                                  `json:"ended"`
		Overflow      bool                                  `json:"terminal_overflow"`
		RetainedBytes string                                `json:"retained_bytes"`
		Terminal      contract.KVHTTPSessionRenewalTerminal `json:"terminal"`
	}{Version: 1, Scope: scopeRecord{strconv.FormatInt(scope.Namespace.ProfileID, 10), strconv.FormatUint(scope.Generation, 10), scope.Namespace, scope.Prefix, eventHeader(scope.Header), scope.Version, scope.HTTP}, Authored: authored, Ended: snapshot.Ended, Overflow: snapshot.TerminalOverflow,
		RetainedBytes: strconv.FormatInt(snapshot.RetainedBytes, 10), Terminal: snapshot.Terminal}
	record.Request = HTTPSessionRenewalInput{r.SessionID, r.Interval.String(), strconv.Itoa(r.MaxRenewals), strconv.FormatInt(r.MaxBytes, 10)}
	for _, o := range snapshot.Observations {
		h := o.HTTP
		raw, err := encodeHTTPOperationReceipt(Receipt{Scope: scope, Authored: authored, Result: contract.KVResult{HTTP: &h}})
		if err != nil {
			return nil, err
		}
		item := observationRecord{Original: raw, Sequence: o.Sequence, ReceivedAt: o.ReceivedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), ProjectionComplete: o.ProjectionComplete, ProjectionError: o.ProjectionError}
		if o.ProjectionComplete {
			item.Session = &sessionRecord{o.Session.ID, o.Session.Name, o.Session.Node, o.Session.TTL, o.Session.Behavior, strconv.FormatUint(o.Session.LockDelay, 10)}
		}
		record.Observations = append(record.Observations, item)
	}
	return json.Marshal(record)
}

// Capture a complete immutable snapshot on the owning UI thread before a
// picker, clipboard job or disk worker can wait. This never consumes Poll.
func (s *State) HTTPSessionRenewalRecord(d *Document) ([]byte, error) {
	if !s.owned(d) || !d.httpSessionRenewalContent() {
		return nil, jsondocument.ErrInvalid
	}
	w := d.httpSessionRenewal
	return encodeHTTPSessionRenewal(w.evidenceScope.Clone(), w.authored, w.snapshot())
}
func (s *State) ExportHTTPSessionRenewalRecord(d *Document) {
	if !s.active(d) || d.FileBusy() || !d.httpSessionRenewalContent() {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	w := d.httpSessionRenewal
	scope, authored, snapshot := w.evidenceScope.Clone(), w.authored, w.snapshot()
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		raw, err := encodeHTTPSessionRenewal(scope, authored, snapshot)
		if err != nil {
			return fileResult{err: err}
		}
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath("http-session-renewal-" + strconv.FormatUint(scope.Generation, 10) + ".nyakv-receipt.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
func (s *State) CopyHTTPSessionRenewalRecord(services ui.Services, d *Document) {
	if !s.active(d) || d.FileBusy() || !d.httpSessionRenewalContent() {
		return
	}
	w := d.httpSessionRenewal
	scope, authored, snapshot := w.evidenceScope.Clone(), w.authored, w.snapshot()
	s.fileJob(d, "copy-http-session-renewal", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		raw, err := encodeHTTPSessionRenewal(scope, authored, snapshot)
		if err == nil {
			err = ctx.Err()
		}
		return fileResult{clipboard: string(raw), err: err}
	})
	d.io.clipboard = services.WriteClipboard
}

func (d *Document) HTTPSessionRenewalStatus() contract.KVHTTPSessionRenewalStatus {
	if d.httpSessionRenewal == nil {
		return contract.KVHTTPSessionRenewalStatus{}
	}
	return d.httpSessionRenewal.status
}
