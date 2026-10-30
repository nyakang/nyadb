package datasources

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

type mdbSource struct {
	config  Config
	ctx     context.Context
	stop    context.CancelFunc
	mu      sync.Mutex
	closed  bool
	readers map[*mdbRows]bool
}

func openMDBTools(ctx context.Context, c Config) (Source, error) {
	for _, n := range []string{"mdb-tables", "mdb-schema", "mdb-json"} {
		if _, ok := tool(c, n); !ok {
			return nil, unavailable("MDBTools")
		}
	}
	c.ConnectionString = ""
	c.Driver = ""
	life, stop := context.WithCancel(context.Background())
	s := &mdbSource{config: c, ctx: life, stop: stop, readers: map[*mdbRows]bool{}}
	if _, err := s.Tables(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Native helper diagnostics are discarded rather than logged: MDBTools can
// echo database paths, table names and malformed source values on stderr.
func (s *mdbSource) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if s.ctx.Err() != nil {
		return nil, ErrClosed
	}
	path, ok := tool(s.config, name)
	if !ok {
		return nil, unavailable("MDBTools")
	}
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	cmd := exec.CommandContext(operation, path, args...)
	cmd.Stderr = io.Discard
	out := &limitedOutput{limit: 2 << 20}
	cmd.Stdout = out
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.ctx.Err() != nil {
		return nil, ErrClosed
	}
	if err != nil {
		if out.full {
			return nil, ErrValueLimit
		}
		return nil, safeError("MDBTools", "cannot inspect source", nil)
	}
	return out.buffer.Bytes(), nil
}

type limitedOutput struct {
	buffer bytes.Buffer
	limit  int
	full   bool
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		w.full = true
		return 0, ErrValueLimit
	}
	return w.buffer.Write(p)
}
func (s *mdbSource) Tables(ctx context.Context) ([]Table, error) {
	b, e := s.command(ctx, "mdb-tables", "-1", "--", s.config.Path)
	if e != nil {
		return nil, e
	}
	out := []Table{}
	for _, n := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if n == "" {
			continue
		}
		t := Table{Name: n}
		if validateTable(t) != nil {
			return nil, ErrInvalidSource
		}
		out = append(out, t)
		if len(out) > 10000 {
			return nil, ErrValueLimit
		}
	}
	return out, nil
}

var mdbSchemaColumn = regexp.MustCompile(`^\s*\[((?:\]\]|[^\]])*)\]\s+(.+?)\s*,?\s*$`)

func (s *mdbSource) Columns(ctx context.Context, t Table) ([]Column, error) {
	if e := validateTable(t); e != nil {
		return nil, e
	}
	if t.Catalog != "" || t.Schema != "" {
		return nil, ErrInvalidSource
	}
	b, e := s.command(ctx, "mdb-schema", "--not-null", "--no-default-values", "--no-indexes", "--no-relations", "--no-comments", "-T", t.Name, "--", s.config.Path, "access")
	if e != nil {
		return nil, e
	}
	cols := []Column{}
	for _, line := range strings.Split(string(b), "\n") {
		m := mdbSchemaColumn.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.ReplaceAll(m[1], `]]`, `]`)
		typ := strings.TrimSuffix(strings.TrimSpace(m[2]), ",")
		nullable := !strings.Contains(strings.ToUpper(typ), "NOT NULL")
		typ = strings.TrimSpace(strings.ReplaceAll(typ, "NOT NULL", ""))
		cols = append(cols, Column{Name: name, Type: typ, Nullable: nullable})
	}
	if e = checkedColumns(cols); e != nil {
		return nil, e
	}
	return cols, nil
}
func (s *mdbSource) Rows(ctx context.Context, t Table) (RowReader, error) {
	cols, e := s.Columns(ctx, t)
	if e != nil {
		return nil, e
	}
	path, ok := tool(s.config, "mdb-json")
	if !ok {
		return nil, unavailable("MDBTools")
	}
	life, cancel := context.WithCancel(s.ctx)
	cmd := exec.CommandContext(life, path, "-D", "%Y-%m-%d", "-T", "%Y-%m-%d %H:%M:%S", "--", s.config.Path, t.Name)
	cmd.Stderr = io.Discard
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		cancel()
		return nil, safeError("MDBTools", "cannot open source reader", nil)
	}
	if e = cmd.Start(); e != nil {
		cancel()
		pipe.Close()
		return nil, safeError("MDBTools", "cannot start source reader", nil)
	}
	r := &mdbRows{source: s, cmd: cmd, pipe: pipe, reader: bufio.NewReaderSize(pipe, 64<<10), stop: cancel, columns: cols}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		r.Close()
		return nil, ErrClosed
	}
	s.readers[r] = true
	s.mu.Unlock()
	return r, nil
}
func (s *mdbSource) Close() error {
	s.stop()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	readers := make([]*mdbRows, 0, len(s.readers))
	for r := range s.readers {
		readers = append(readers, r)
	}
	s.readers = nil
	s.mu.Unlock()
	for _, r := range readers {
		r.Close()
	}
	return nil
}

type mdbRows struct {
	source         *mdbSource
	cmd            *exec.Cmd
	pipe           io.ReadCloser
	reader         *bufio.Reader
	stop           context.CancelFunc
	columns        []Column
	mu             sync.Mutex
	closed, waited bool
	waitErr        error
}

func (r *mdbRows) Columns() []Column { return append([]Column(nil), r.columns...) }
func (r *mdbRows) Next(ctx context.Context) ([]Value, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	done := make(chan struct{})
	ack := make(chan struct{})
	go func() {
		defer close(ack)
		select {
		case <-done:
			return
		case <-ctx.Done():
			r.stop()
		}
	}()
	var line []byte
	var readErr error
	for {
		part, e := r.reader.ReadSlice('\n')
		if len(line)+len(part) > MaxRowBytes*6+4096 {
			r.stop()
			readErr = ErrValueLimit
			break
		}
		line = append(line, part...)
		if e != bufio.ErrBufferFull {
			readErr = e
			break
		}
	}
	close(done)
	<-ack
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if r.source.ctx.Err() != nil {
		return nil, ErrClosed
	}
	if readErr != nil && readErr != io.EOF {
		return nil, safeError("MDBTools", "cannot read complete source row", readErr)
	}
	if len(bytes.TrimSpace(line)) == 0 {
		if !r.waited {
			r.waitErr = r.cmd.Wait()
			r.waited = true
		}
		if r.waitErr != nil {
			return nil, safeError("MDBTools", "source reader failed", nil)
		}
		return nil, io.EOF
	}
	return decodeMDBRow(line, r.columns)
}
func decodeMDBRow(line []byte, columns []Column) ([]Value, error) {
	if !utf8.Valid(line) {
		return nil, ErrInvalidSource
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(line, &fields); e != nil {
		return nil, safeError("MDBTools", "invalid source row encoding", ErrInvalidSource)
	}
	allowed := map[string]bool{}
	for _, c := range columns {
		allowed[c.Name] = true
	}
	for name := range fields {
		if !allowed[name] {
			return nil, ErrInvalidSource
		}
	}
	row := make([]Value, len(columns))
	for i, c := range columns {
		raw, ok := fields[c.Name]
		if !ok || bytes.Equal(raw, []byte("null")) {
			row[i].Null = true
			continue
		}
		switch raw[0] {
		case '"':
			if e := json.Unmarshal(raw, &row[i].Text); e != nil {
				return nil, ErrInvalidSource
			} // MDBTools binds ordinary text/memo at 200000 bytes. Reject a boundary value
			// instead of accepting a potentially truncated value from an older helper.
			if len(row[i].Text) >= 199999 {
				return nil, safeError("MDBTools", "value reaches the helper's complete-text limit", ErrValueLimit)
			}
		case '{':
			var b struct {
				Data string `json:"$binary"`
				Type string `json:"$type"`
			}
			var keys map[string]json.RawMessage
			if json.Unmarshal(raw, &b) != nil || json.Unmarshal(raw, &keys) != nil || len(keys) != 2 || b.Type != "00" {
				return nil, ErrInvalidSource
			}
			data, e := base64.StdEncoding.Strict().DecodeString(b.Data)
			if e != nil {
				return nil, ErrInvalidSource
			}
			row[i] = Value{Binary: true, Bytes: data}
			if row[i].Bytes == nil {
				row[i].Bytes = []byte{}
			}
		default:
			if bytes.Equal(raw, []byte("true")) {
				row[i].Text = "1"
			} else if bytes.Equal(raw, []byte("false")) {
				row[i].Text = "0"
			} else {
				var n json.Number
				if json.Unmarshal(raw, &n) != nil {
					return nil, ErrInvalidSource
				}
				row[i].Text = string(n)
			}
		}
	}
	if e := checkRow(row); e != nil {
		return nil, e
	}
	return row, nil
}
func (r *mdbRows) Close() error {
	r.stop()
	r.pipe.Close()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if !r.waited {
		r.waitErr = r.cmd.Wait()
		r.waited = true
	}
	r.mu.Unlock()
	r.source.mu.Lock()
	delete(r.source.readers, r)
	r.source.mu.Unlock()
	return nil
}
