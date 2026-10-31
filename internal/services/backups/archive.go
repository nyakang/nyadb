// Package backups defines NyaDB's independent, versioned backup format.
package backups

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const Version = 1
const MaxRowBytes = 32 << 20
const MaxObjects = 10000

var ErrInvalid = errors.New("invalid or incomplete NyaDB backup")

type Object struct {
	Kind, Name, Table, Engine, SQLMode string
	DDL, Data, DDLHash, DataHash       string
	Columns                            []contract.Column
	Rows                               int64
	Consistency                        string
	Dependencies                       []string
}
type Manifest struct {
	Format           string
	Version          int
	Database, Server string
	Created          time.Time
	Complete         bool
	Objects          []Object
	Warnings         []string
}
type Writer struct {
	archive  *zip.Writer
	manifest Manifest
	failed   error
}

func NewWriter(w io.Writer, database, server string) *Writer {
	return &Writer{archive: zip.NewWriter(w), manifest: Manifest{Format: "nyadb-backup", Version: Version, Database: database, Server: server, Created: time.Now().UTC()}}
}
func (w *Writer) Warning(s string) { w.manifest.Warnings = append(w.manifest.Warnings, s) }
func (w *Writer) Add(ctx context.Context, object Object, ddl string, rows func(func([]contract.Cell) error) error) error {
	if w.failed != nil {
		return w.failed
	}
	err := w.add(ctx, object, ddl, rows)
	if err != nil {
		w.failed = err
	}
	return err
}
func (w *Writer) add(ctx context.Context, o Object, ddl string, rows func(func([]contract.Cell) error) error) error {
	if len(w.manifest.Objects) >= MaxObjects || len(ddl) > 8<<20 || ddl == "" || o.Name == "" {
		return ErrInvalid
	}
	for _, old := range w.manifest.Objects {
		if old.Kind == o.Kind && old.Name == o.Name {
			return ErrInvalid
		}
	}
	names := map[string]bool{}
	for _, c := range o.Columns {
		if c.Name == "" || names[c.Name] {
			return ErrInvalid
		}
		names[c.Name] = true
	}
	index := len(w.manifest.Objects)
	o.DDL = fmt.Sprintf("objects/%05d.sql", index)
	o.DDLHash = ""
	o.Data = ""
	o.DataHash = ""
	o.Rows = 0
	f, e := w.archive.Create(o.DDL)
	if e != nil {
		return e
	}
	h := sha256.New()
	if _, e = io.WriteString(io.MultiWriter(f, h), ddl); e != nil {
		return e
	}
	o.DDLHash = hex.EncodeToString(h.Sum(nil))
	if rows != nil {
		o.Data = fmt.Sprintf("objects/%05d.jsonl", index)
		f, e = w.archive.Create(o.Data)
		if e != nil {
			return e
		}
		h = sha256.New()
		enc := json.NewEncoder(io.MultiWriter(f, h))
		e = rows(func(row []contract.Cell) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if e := validateRow(row, len(o.Columns)); e != nil {
				return e
			}
			if e := enc.Encode(row); e != nil {
				return e
			}
			o.Rows++
			return nil
		})
		if e != nil {
			return e
		}
		o.DataHash = hex.EncodeToString(h.Sum(nil))
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	w.manifest.Objects = append(w.manifest.Objects, o)
	return nil
}
func (w *Writer) Finish(ctx context.Context) (Manifest, error) {
	if w.failed != nil {
		_ = w.archive.Close()
		return Manifest{}, w.failed
	}
	if e := ctx.Err(); e != nil {
		_ = w.archive.Close()
		return Manifest{}, e
	}
	w.manifest.Complete = true
	f, e := w.archive.Create("manifest.json")
	if e == nil {
		e = json.NewEncoder(f).Encode(w.manifest)
	}
	if e != nil {
		_ = w.archive.Close()
		return Manifest{}, e
	}
	if e = w.archive.Close(); e != nil {
		return Manifest{}, e
	}
	return w.manifest, ctx.Err()
}
func validateRow(row []contract.Cell, columns int) error {
	if len(row) != columns || columns == 0 || columns > 128 {
		return ErrInvalid
	}
	size, encoded := 0, 2
	for _, c := range row {
		if c.Truncated {
			return errors.New("truncated value cannot enter a complete backup")
		}
		switch c.Kind {
		case contract.Null:
			if c.Text != "" || len(c.Bytes) > 0 {
				return ErrInvalid
			}
		case contract.Binary:
			if c.Text != "" {
				return ErrInvalid
			}
		case contract.Text, contract.Integer, contract.Decimal, contract.Floating, contract.Boolean, contract.DateTime, contract.JSON:
			if len(c.Bytes) > 0 || !utf8.ValidString(c.Text) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		size += len(c.Text) + len(c.Bytes)
		encoded += 128 + 4*((len(c.Bytes)+2)/3)
		for _, r := range c.Text {
			switch {
			case r < 32 || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029:
				encoded += 6
			case r == '"' || r == '\\':
				encoded += 2
			default:
				encoded += utf8.RuneLen(r)
			}
		}
		if encoded >= MaxRowBytes {
			return errors.New("encoded backup row exceeds the 32 MiB format limit")
		}
		// Same bounded raw-record envelope accepted by the streaming importer.
		if size > 16<<20 {
			return errors.New("backup record exceeds the 16 MiB streaming limit")
		}
	}
	return nil
}

type Reader struct {
	file     *os.File
	archive  *zip.Reader
	manifest Manifest
	entries  map[string]*zip.File
}

func Open(ctx context.Context, path string) (*Reader, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	stat, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	z, e := zip.NewReader(f, stat.Size())
	if e != nil {
		f.Close()
		return nil, e
	}
	r := &Reader{file: f, archive: z, entries: make(map[string]*zip.File)}
	fail := func(e error) (*Reader, error) { f.Close(); return nil, e }
	if len(z.File) > 2*MaxObjects+1 {
		return fail(ErrInvalid)
	}
	for _, v := range z.File {
		if r.entries[v.Name] != nil {
			return fail(ErrInvalid)
		}
		r.entries[v.Name] = v
	}
	mf := r.entries["manifest.json"]
	if mf == nil || mf.UncompressedSize64 > 16<<20 {
		return fail(ErrInvalid)
	}
	raw, e := mf.Open()
	if e != nil {
		return fail(e)
	}
	dec := json.NewDecoder(io.LimitReader(raw, 16<<20))
	dec.DisallowUnknownFields()
	e = dec.Decode(&r.manifest)
	if e == nil {
		var extra any
		if end := dec.Decode(&extra); end != io.EOF {
			e = ErrInvalid
		}
	}
	raw.Close()
	if e != nil {
		return fail(e)
	}
	m := r.manifest
	if m.Format != "nyadb-backup" || m.Version != Version || !m.Complete || m.Database == "" || len(m.Objects) > MaxObjects {
		return fail(ErrInvalid)
	}
	used := map[string]bool{"manifest.json": true}
	identities := map[string]bool{}
	for i, o := range m.Objects {
		key := o.Kind + "\x00" + o.Name
		if identities[key] || o.Name == "" || o.Rows < 0 {
			return fail(ErrInvalid)
		}
		identities[key] = true
		if o.DDL != fmt.Sprintf("objects/%05d.sql", i) || len(o.Columns) > 128 {
			return fail(ErrInvalid)
		}
		names := map[string]bool{}
		for _, c := range o.Columns {
			if c.Name == "" || names[c.Name] {
				return fail(ErrInvalid)
			}
			names[c.Name] = true
		}
		if e = r.check(ctx, o.DDL, o.DDLHash, 8<<20); e != nil {
			return fail(e)
		}
		used[o.DDL] = true
		if o.Data != "" {
			if o.Kind != "TABLE" || o.Data != fmt.Sprintf("objects/%05d.jsonl", i) {
				return fail(ErrInvalid)
			}
			if e = r.check(ctx, o.Data, o.DataHash, 0); e != nil {
				return fail(e)
			}
			used[o.Data] = true
			source, e := r.Rows(i)
			if e != nil {
				return fail(e)
			}
			count := int64(0)
			for {
				row, e := source.Next(ctx)
				if e == io.EOF {
					break
				}
				if e != nil {
					source.Close()
					return fail(e)
				}
				if e = validateRow(row, len(o.Columns)); e != nil {
					source.Close()
					return fail(e)
				}
				count++
				if count > o.Rows {
					source.Close()
					return fail(ErrInvalid)
				}
			}
			if e = source.Close(); e != nil {
				return fail(e)
			}
			if count != o.Rows {
				return fail(ErrInvalid)
			}
		} else if o.Rows != 0 || o.DataHash != "" {
			return fail(ErrInvalid)
		}
	}
	if len(used) != len(r.entries) {
		return fail(ErrInvalid)
	}
	return r, nil
}
func (r *Reader) Close() error { return r.file.Close() }
func (r *Reader) Manifest() Manifest {
	b, _ := json.Marshal(r.manifest)
	var m Manifest
	_ = json.Unmarshal(b, &m)
	return m
}
func (r *Reader) check(ctx context.Context, name, sum string, limit uint64) error {
	entry := r.entries[name]
	if entry == nil || len(sum) != 64 || limit > 0 && entry.UncompressedSize64 > limit {
		return ErrInvalid
	}
	f, e := entry.Open()
	if e != nil {
		return e
	}
	defer f.Close()
	h := sha256.New()
	b := make([]byte, 64<<10)
	for {
		if e = ctx.Err(); e != nil {
			return e
		}
		n, re := f.Read(b)
		if n > 0 {
			_, _ = h.Write(b[:n])
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			return re
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		return errors.New("backup checksum mismatch: " + name)
	}
	return nil
}
func (r *Reader) DDL(index int) (string, error) {
	if index < 0 || index >= len(r.manifest.Objects) {
		return "", ErrInvalid
	}
	o := r.manifest.Objects[index]
	if e := r.check(context.Background(), o.DDL, o.DDLHash, 8<<20); e != nil {
		return "", e
	}
	f, e := r.entries[o.DDL].Open()
	if e != nil {
		return "", e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if len(b) > 8<<20 {
		return "", ErrInvalid
	}
	return string(b), e
}

type Rows struct {
	reader  io.ReadCloser
	scanner *bufio.Scanner
	columns int
	hash    hash.Hash
	want    string
	done    bool
}

func (r *Reader) Rows(index int) (*Rows, error) {
	if index < 0 || index >= len(r.manifest.Objects) {
		return nil, ErrInvalid
	}
	o := r.manifest.Objects[index]
	f := r.entries[o.Data]
	if f == nil {
		return nil, ErrInvalid
	}
	raw, e := f.Open()
	if e != nil {
		return nil, e
	}
	h := sha256.New()
	s := bufio.NewScanner(io.TeeReader(raw, h))
	s.Buffer(make([]byte, 64<<10), MaxRowBytes)
	return &Rows{reader: raw, scanner: s, columns: len(o.Columns), hash: h, want: o.DataHash}, nil
}
func (r *Rows) Next(ctx context.Context) ([]contract.Cell, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if r.done {
		return nil, io.EOF
	}
	if !r.scanner.Scan() {
		r.done = true
		if e := r.scanner.Err(); e != nil {
			return nil, e
		}
		if hex.EncodeToString(r.hash.Sum(nil)) != r.want {
			return nil, ErrInvalid
		}
		return nil, io.EOF
	}
	var cells []contract.Cell
	if e := json.Unmarshal(r.scanner.Bytes(), &cells); e != nil {
		return nil, e
	}
	if e := validateRow(cells, r.columns); e != nil {
		return nil, e
	}
	return cells, nil
}
func (r *Rows) Close() error { return r.reader.Close() }
