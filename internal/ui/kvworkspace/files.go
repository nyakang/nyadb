package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

// WriteStream must atomically publish a file only if its writer callback
// succeeds. File pickers may finish after cancellation; work checks the original
// context again before opening/reading/writing any selected path.
type Files struct {
	OpenPath    func() (string, error)
	SavePath    func(string) (string, error)
	OpenReader  func(string) (io.ReadCloser, error)
	WriteStream func(string, func(io.Writer) error) error
}
type fileResult struct {
	clipboard string
	path      string
	request   contract.KVRequest
	err       error
}
type fileOperation struct {
	clipboard func(string)
	canceled  bool
	kind      string
	before    contract.KVRequest
	cancel    context.CancelFunc
	out       <-chan fileResult
	finished  <-chan struct{}
}

const requestFileLimit = 100 << 20

func EncodeRequest(r contract.KVRequest) ([]byte, error) {
	if len(r.JSON) > inputLimit || !utf8.ValidString(r.JSON) {
		return nil, jsondocument.ErrBounds
	}
	supported := eventMethod(r.Method)
	for _, m := range methods {
		if m.Path == r.Method {
			supported = true
		}
	}
	if !supported {
		return nil, jsondocument.ErrInvalid
	}
	return json.Marshal(struct {
		Version int    `json:"version"`
		Method  string `json:"method"`
		Text    string `json:"text"`
	}{1, r.Method, r.JSON})
}
func DecodeRequest(raw []byte) (contract.KVRequest, error) {
	var fields map[string]json.RawMessage
	if err := jsondocument.Decode(raw, &fields, requestFileLimit); err != nil {
		return contract.KVRequest{}, err
	}
	if len(fields) != 3 || string(fields["version"]) != "1" {
		return contract.KVRequest{}, jsondocument.ErrInvalid
	}
	for _, key := range []string{"method", "text"} {
		if fields[key] == nil || string(fields[key]) == "null" {
			return contract.KVRequest{}, jsondocument.ErrInvalid
		}
	}
	var r contract.KVRequest
	if json.Unmarshal(fields["method"], &r.Method) != nil || json.Unmarshal(fields["text"], &r.JSON) != nil {
		return r, jsondocument.ErrInvalid
	}
	if _, err := EncodeRequest(r); err != nil {
		return r, err
	}
	return r, nil
}
func (s *State) fileJob(d *Document, kind string, work func(context.Context) fileResult) {
	ctx, cancel := context.WithCancel(d.ctx)
	out := make(chan fileResult, 1)
	finished := make(chan struct{})
	d.io = &fileOperation{kind: kind, before: d.Request(), cancel: cancel, out: out, finished: finished}
	invalidate := s.Invalidate
	go func() {
		defer close(finished)
		defer cancel()
		r := work(ctx)
		out <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) OpenRequest(d *Document) {
	if !s.active(d) || d.io != nil || d.Busy() {
		return
	}
	f := s.deps.Files
	if f.OpenPath == nil || f.OpenReader == nil {
		return
	}
	d.confirmation = nil
	s.fileJob(d, "open", func(ctx context.Context) fileResult {
		path, err := f.OpenPath()
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		if err = ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		reader, err := f.OpenReader(path)
		if err != nil {
			return fileResult{err: err}
		}
		defer reader.Close()
		raw, err := io.ReadAll(io.LimitReader(reader, requestFileLimit+1))
		if err != nil {
			return fileResult{err: err}
		}
		if err = ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		request, err := DecodeRequest(raw)
		return fileResult{path: path, request: request, err: err}
	})
}
func writeCaptured(ctx context.Context, f Files, path string, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.WriteStream(path, func(w io.Writer) error {
		for len(raw) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			count := min(len(raw), 64<<10)
			n, err := w.Write(raw[:count])
			if err != nil {
				return err
			}
			if n != count {
				return io.ErrShortWrite
			}
			raw = raw[n:]
		}
		return ctx.Err()
	})
}
func (s *State) SaveRequest(d *Document, as bool) {
	if !s.active(d) || d.io != nil {
		return
	}
	f := s.deps.Files
	if f.WriteStream == nil {
		return
	}
	captured := d.Request()
	raw, err := EncodeRequest(captured)
	if err != nil {
		d.error = locales.Text("KV 请求文件无效")
		return
	}
	path := d.path
	if as {
		path = ""
	}
	if path == "" && f.SavePath == nil {
		return
	}
	s.fileJob(d, "save", func(ctx context.Context) fileResult {
		if path == "" {
			var err error
			path, err = f.SavePath(draftFilename(captured.Method))
			if err != nil || path == "" {
				return fileResult{err: err}
			}
		}
		err := writeCaptured(ctx, f, path, raw)
		return fileResult{path: path, request: captured, err: err}
	})
}
func draftFilename(method string) string {
	if method == WatchMethod {
		return "Watch.nyakv"
	}
	if method == HistoryMethod {
		return "History.nyakv"
	}
	for _, m := range methods {
		if m.Path == method {
			return m.Name + ".nyakv"
		}
	}
	return "request.nyakv"
}
func (s *State) selectedReceipt(d *Document) *Receipt {
	if d.selectedUnknown && d.unknown != nil {
		return d.unknown
	}
	return d.last
}
func (s *State) ReceiptJSON(d *Document) (string, bool) {
	if !s.owned(d) {
		return "", false
	}
	r := s.selectedReceipt(d)
	if r == nil || r.Result.JSON == "" {
		return "", false
	}
	return r.Result.JSON, true
}
func (s *State) ExportReceipt(d *Document, native bool) {
	if !s.active(d) || d.io != nil {
		return
	}
	r := s.selectedReceipt(d)
	if r == nil {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	name := "response.json"
	raw := []byte(r.Result.JSON)
	if native {
		name = "response.pb"
		raw = bytes.Clone(r.Result.Native)
	}
	if len(raw) == 0 {
		d.error = locales.Text("此回执没有可导出的原生内容")
		return
	}
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		path, err := f.SavePath(name)
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
func (s *State) CancelFile(d *Document) {
	if s.owned(d) && d.io != nil {
		d.io.canceled = true
		d.io.cancel()
	}
}
func (s *State) consumeFiles(d *Document) {
	if d.io == nil {
		return
	}
	select {
	case r := <-d.io.out:
		job := d.io
		d.io = nil
		job.cancel()
		if r.err != nil {
			if !errors.Is(r.err, context.Canceled) {
				d.error = locales.Text("KV 文件操作失败") + ": " + r.err.Error()
			}
			return
		}
		if job.kind == "copy-events" {
			if !job.canceled && job.clipboard != nil {
				job.clipboard(r.clipboard)
			}
			return
		}
		if r.path == "" {
			return
		}
		if job.kind == "open" {
			if d.Request() != job.before {
				d.error = locales.Text("KV 请求已更改，未载入所选文件")
				return
			}
			d.method = r.request.Method
			d.Editor.Buffer.Set(r.request.JSON)
			d.path = r.path
			d.saved = r.request
			d.permit = nil
		} else if job.kind == "save" {
			d.path = r.path
			d.saved = r.request
		}
	default:
	}
}

func (s *State) SetFiles(files Files) { s.deps.Files = files }
