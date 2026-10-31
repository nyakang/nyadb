package sqlworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nyakang/nyadb/locales"
	"io"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type Files struct {
	OpenReader func(string) (io.ReadCloser, error)

	WriteStream func(string, func(io.Writer) error) error
	OpenPath    func() (string, error)
	SavePath    func(string) (string, error)
	Read        func(string) (string, error)
	Write       func(string, string) error
}
type ioResult struct {
	kind, path, text string
	named            queries.Named
	workspace        queries.Workspace
	names            []queries.Named
	revision         uint64
	err              error
}

func (s *State) PersistenceLoading() bool { return s.persistence.loading }

type ioJob struct {
	doc  *Document
	out  <-chan ioResult
	next func(ui.Services)
}
type persistence struct {
	closed            bool
	timer             *time.Timer
	savedRevision     uint64
	flushNext         func(ui.Services)
	awaitClose        func(ui.Services)
	explainDoc        *Document
	explainSQL        string
	explainOpen       bool
	store             *queries.Store
	files             Files
	jobs              []*ioJob
	names             []queries.Named
	revision          uint64
	signature         string
	changed           time.Time
	pending           <-chan ioResult
	loading           bool
	blocked           bool
	restored          func(queries.Workspace)
	nameOpen          bool
	name              string
	nameDoc           *Document
	guardDoc          *Document
	guardNext         func(ui.Services)
	exitOpen, keeping bool
	exitNext          func(ui.Services)
	lastWorkspace     queries.Workspace
}

func (d *Document) Name() string {
	if d.name != "" {
		return d.name
	}
	return fmt.Sprintf("SQL %d", d.id)
}
func (d *Document) Dirty() bool { return d.Editor != nil && d.Editor.Buffer.String() != d.savedText }
func (s *State) SetPersistence(store *queries.Store, files Files, restored func(queries.Workspace)) {
	s.persistence.store, s.persistence.files, s.persistence.restored = store, files, restored
	if store == nil {
		return
	}
	s.persistence.loading = true
	s.startIO(nil, nil, func() ioResult {
		w, e := store.LoadWorkspace(context.Background())
		if e != nil {
			return ioResult{kind: "load", err: e}
		}
		names, e := store.ListNamed(context.Background(), 0)
		return ioResult{kind: "load", workspace: w, names: names, err: e}
	})
}
func (s *State) RestoreDocument(snapshot queries.Document) *Document {
	d := s.Open(snapshot.ID, snapshot.Text, 0, "", false)
	d.name, d.path, d.queryID, d.savedText = snapshot.Name, snapshot.Path, snapshot.QueryID, snapshot.SavedText
	d.revision = snapshot.Revision
	d.namedRevision = snapshot.NamedRevision
	d.recoveryProfile, d.recoverySchema = snapshot.ProfileID, snapshot.Database
	d.recoveryNeeded = snapshot.ProfileID > 0
	d.Editor.Select(max(0, min(snapshot.Anchor, len(snapshot.Text))), max(0, min(snapshot.Cursor, len(snapshot.Text))))
	return d
}
func (s *State) Snapshot(active uint64, order []uint64) queries.Workspace {
	w := queries.Workspace{ActiveID: active}
	for _, id := range order {
		d := s.documents[id]
		if d == nil || d.preview || d.closed || d.Editor == nil {
			continue
		}
		version := d.Editor.Buffer.Version()
		if d.bufferVersion != version {
			d.bufferVersion = version
			d.revision++
			d.text = d.Editor.Buffer.String()
		}
		profile, db := d.profileID, d.database
		if d.recoveryNeeded {
			profile, db = d.recoveryProfile, d.recoverySchema
		}
		selection := d.Editor.Selection()
		w.Documents = append(w.Documents, queries.Document{ID: id, QueryID: d.queryID, Name: d.name, Path: d.path, Text: d.text, SavedText: d.savedText, ProfileID: profile, Database: db, Cursor: selection.Head, Anchor: selection.Anchor, Revision: d.revision, NamedRevision: d.namedRevision})
	}
	return w
}

// Observe runs on the UI thread. Only immutable snapshots reach a worker.
func (s *State) Observe(active uint64, order []uint64) {
	p := &s.persistence
	if p.store == nil || p.loading || p.blocked || p.closed {
		return
	}
	w := s.Snapshot(active, order)
	signature, _ := json.Marshal(w)
	key := string(signature)
	if key != p.signature {
		p.signature = key
		p.keeping = false
		p.revision++
		p.changed = time.Now()
		invalidate := s.Invalidate
		if invalidate != nil {
			if p.timer != nil {
				p.timer.Stop()
			}
			p.timer = time.AfterFunc(500*time.Millisecond, invalidate)
		}
	}
	w.Revision = p.revision
	p.lastWorkspace = w
	if p.pending == nil && p.changed != (time.Time{}) && time.Since(p.changed) >= 500*time.Millisecond {
		s.saveWorkspace(nil)
	}
}
func (s *State) saveWorkspace(next func(ui.Services)) {
	p := &s.persistence
	if p.store == nil || p.blocked {
		return
	}
	if next != nil {
		p.flushNext = next
	}
	if p.pending != nil {
		return
	}
	if p.revision <= p.savedRevision {
		if p.flushNext != nil {
			ch := make(chan ioResult, 1)
			ch <- ioResult{kind: "workspace", revision: p.savedRevision}
			p.jobs = append(p.jobs, &ioJob{out: ch})
		}
		return
	}
	w := p.lastWorkspace
	w.Revision = p.revision
	p.changed = time.Time{}
	store := p.store
	s.startIO(nil, nil, func() ioResult {
		return ioResult{kind: "workspace", revision: w.Revision, err: store.SaveWorkspace(context.Background(), w)}
	})
	p.pending = s.persistence.jobs[len(s.persistence.jobs)-1].out
}
func (s *State) startIO(d *Document, next func(ui.Services), work func() ioResult) {
	ch := make(chan ioResult, 1)
	s.persistence.jobs = append(s.persistence.jobs, &ioJob{doc: d, out: ch, next: next})
	invalidate := s.Invalidate
	go func() {
		ch <- work()
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) OpenFile(d *Document) {
	f := s.persistence.files
	if f.OpenPath == nil || f.Read == nil || d == nil || d.closed || d.saving {
		return
	}
	if d.Dirty() {
		s.GuardLeave(d, func(_ ui.Services) { s.OpenFile(d) })
		return
	}
	revision := d.Editor.Buffer.Version()
	s.startIO(d, nil, func() ioResult {
		path, e := f.OpenPath()
		if e != nil || path == "" {
			return ioResult{kind: "open", err: e, revision: revision}
		}
		text, e := f.Read(path)
		return ioResult{kind: "open", path: path, text: text, revision: revision, err: e}
	})
}
func (s *State) Save(d *Document, asFile bool, next func(ui.Services)) {
	if d == nil || d.Editor == nil || d.closed || d.saving {
		return
	}
	text, revision := d.Editor.Buffer.String(), d.Editor.Buffer.Version()
	f := s.persistence.files
	if asFile || d.path != "" {
		if f.Write == nil {
			return
		}
		path := d.path
		name := d.Name() + ".sql"
		d.saving = true
		s.startIO(d, next, func() ioResult {
			var e error
			if asFile || path == "" {
				if f.SavePath == nil {
					return ioResult{kind: "save", err: fmt.Errorf("SQL file picker unavailable")}
				}
				path, e = f.SavePath(name)
			}
			if e == nil && path != "" {
				e = f.Write(path, text)
			}
			return ioResult{kind: "save", path: path, text: text, revision: revision, err: e}
		})
		return
	}
	if s.persistence.store == nil || s.persistence.blocked {
		s.Save(d, true, next)
		return
	}
	if d.queryID == "" {
		s.persistence.nameOpen = true
		s.persistence.nameDoc = d
		s.persistence.name = d.Name()
		if next != nil {
			s.persistence.guardNext = next
		}
		return
	}
	n := queries.Named{ID: d.queryID, Name: d.name, Text: text, ProfileID: d.profileID, Database: d.database, Revision: d.namedRevision + 1}
	if d.recoveryNeeded {
		n.ProfileID, n.Database = d.recoveryProfile, d.recoverySchema
	}
	store := s.persistence.store
	d.saving = true
	s.startIO(d, next, func() ioResult {
		return ioResult{kind: "named", named: n, text: text, revision: revision, err: store.SaveNamed(context.Background(), n)}
	})
}
func (s *State) OpenNamed(d *Document, n queries.Named) {
	if d == nil || d.Editor == nil || d.closed || d.loading || d.saving {
		return
	}
	if d.Dirty() {
		s.GuardLeave(d, func(_ ui.Services) { s.OpenNamed(d, n) })
		return
	}
	d.Editor.Select(0, d.Editor.Buffer.Len())
	d.Editor.Insert(n.Text)
	d.name, d.queryID, d.savedText, d.path = n.Name, n.ID, n.Text, ""
	d.namedRevision = n.Revision
	if d.profileID != n.ProfileID || d.database != n.Database || d.recoveryNeeded {
		s.dispose(d)
		d.profileID, d.database = 0, ""
		d.recoveryProfile, d.recoverySchema = n.ProfileID, n.Database
		d.recoveryNeeded = true
		d.result = nil
	}
}
func (s *State) Named(profile int64) []queries.Named {
	var out []queries.Named
	for _, n := range s.persistence.names {
		if n.ProfileID == profile {
			out = append(out, n)
		}
	}
	return out
}
func (s *State) consumeIO(services ui.Services) {
	p := &s.persistence
	if p.closed {
		return
	}
	jobs := p.jobs
	p.jobs = nil
	var kept []*ioJob
	for _, job := range jobs {
		select {
		case r := <-job.out:
			if job.doc != nil && (r.kind == "save" || r.kind == "named") {
				job.doc.saving = false
			}
			if job.out == p.pending {
				p.pending = nil
			}
			if r.kind == "load" {
				p.loading = false
				if r.err == nil {
					p.revision = r.workspace.Revision
					p.savedRevision = r.workspace.Revision
					p.names = r.names
					if p.restored != nil {
						p.restored(r.workspace)
					}
				} else {
					p.blocked = true
				}
			}
			if r.err != nil {
				s.report(r.err.Error())
				if job.doc != nil && !job.doc.closed {
					job.doc.err = r.err.Error()
				}
				if r.kind == "workspace" {
					p.blocked = true
				}
				continue
			}
			if r.kind == "workspace" {
				p.savedRevision = r.revision
				if p.savedRevision < p.revision {
					s.saveWorkspace(nil)
				} else if p.flushNext != nil {
					next := p.flushNext
					p.flushNext = nil
					next(services)
				}
			}
			if r.kind == "named" {
				found := false
				for i, n := range p.names {
					if n.ID == r.named.ID {
						if n.Revision <= r.named.Revision {
							p.names[i] = r.named
						}
						found = true
					}
				}
				if !found {
					p.names = append(p.names, r.named)
				}
			}
			d := job.doc
			if d != nil {
				if d.closed || d.Editor == nil {
					continue
				}
				current := r.revision == d.Editor.Buffer.Version()
				if r.kind == "open" && !current {
					continue
				}
				switch r.kind {
				case "open":
					if r.path == "" {
						continue
					}
					d.Editor.Select(0, d.Editor.Buffer.Len())
					d.Editor.Insert(r.text)
					d.path, d.savedText, d.name, d.queryID = r.path, r.text, "", ""
				case "save":
					if r.path == "" {
						continue
					}
					d.path, d.savedText = r.path, r.text
					d.revision++
				case "named":
					if d.queryID != r.named.ID {
						continue
					}
					d.savedText = r.text
					d.namedRevision = max(d.namedRevision, r.named.Revision)
					d.revision++
				}
				if !current {
					continue
				}
			}
			if job.next != nil {
				job.next(services)
			}
		default:
			kept = append(kept, job)
		}
	}
	p.jobs = append(kept, p.jobs...)
}
func (s *State) GuardLeave(d *Document, next func(ui.Services)) bool {
	if d == nil || !d.Dirty() {
		return false
	}
	s.persistence.guardDoc, s.persistence.guardNext = d, next
	return true
}
func (s *State) GuardWorkspaceClose(next func(ui.Services)) bool {
	if len(s.persistence.jobs) > 0 {
		s.persistence.awaitClose = next
		return true
	}
	if s.persistence.keeping {
		return false
	}
	for _, d := range s.documents {
		if !d.preview && d.Dirty() {
			s.persistence.exitOpen = true
			s.persistence.exitNext = next
			return true
		}
	}
	if p := &s.persistence; p.store != nil && !p.blocked && !p.loading && p.revision > p.savedRevision {
		s.saveWorkspace(func(services ui.Services) { p.keeping = true; next(services) })
		return true
	}
	return false
}
func (s *State) Modals(c *ui.Context) {
	s.explainModal(c)
	p := &s.persistence
	if p.guardDoc != nil {
		d := p.guardDoc
		open := true
		ui.Modal(c, &open, func() {
			ui.Column(c).Width(420).Padding(18).Gap(12).Children(func() {
				ui.Text(c, locales.Text("保存查询更改？"))
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "保存").Clicked() {
						next := p.guardNext
						p.guardDoc = nil
						p.guardNext = nil
						s.Save(d, false, next)
					}
					if ui.Button(c, locales.Text("放弃")).Clicked() {
						d.Editor.Select(0, d.Editor.Buffer.Len())
						d.Editor.Insert(d.savedText)
						next := p.guardNext
						p.guardDoc = nil
						p.guardNext = nil
						if next != nil {
							next(c.Services())
						}
					}
					if ui.Button(c, "取消").Clicked() {
						p.guardDoc = nil
						p.guardNext = nil
					}
				})
			})
		})
		if !open {
			p.guardDoc = nil
			p.guardNext = nil
		}
	}
	if p.nameOpen {
		ui.Modal(c, &p.nameOpen, func() {
			ui.Column(c).Width(420).Padding(18).Gap(10).Children(func() {
				ui.Text(c, locales.Text("保存命名查询"))
				ui.TextInput(c, &p.name).Label(locales.Text("查询名称"))
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "取消").Clicked() {
						p.nameOpen = false
						p.guardNext = nil
					}
					if ui.PrimaryButton(c, locales.Text("保存查询")).Disabled(strings.TrimSpace(p.name) == "").Clicked() {
						d := p.nameDoc
						d.name = strings.TrimSpace(p.name)
						d.queryID = queries.NewID()
						next := p.guardNext
						p.guardNext = nil
						p.nameOpen = false
						s.Save(d, false, next)
					}
				})
			})
		})
	}
	if p.exitOpen {
		ui.Modal(c, &p.exitOpen, func() {
			ui.Column(c).Width(460).Padding(18).Gap(12).Children(func() {
				ui.Text(c, locales.Text("退出前保存查询"))
				ui.Text(c, locales.Text("未保存文本可加密保留，重新打开后不会执行 SQL。"))
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("保留草稿并退出")).Disabled(p.store == nil || p.blocked || p.loading).Clicked() {
						p.exitOpen = false
						s.saveWorkspace(func(services ui.Services) {
							p.keeping = true
							if p.exitNext != nil {
								p.exitNext(services)
							}
						})
					}
					if ui.Button(c, locales.Text("逐页保存")).Clicked() {
						p.exitOpen = false
						for _, d := range s.documents {
							if d.Dirty() {
								s.GuardLeave(d, p.exitNext)
								break
							}
						}
					}
					if ui.Button(c, "取消").Clicked() {
						p.exitOpen = false
						p.exitNext = nil
					}
				})
			})
		})
	}
}

func (s *State) ConsumeDocuments(services ui.Services) {
	if s.persistence.closed {
		return
	}
	s.consumeIO(services)
	p := &s.persistence
	if len(p.jobs) == 0 && p.awaitClose != nil {
		next := p.awaitClose
		p.awaitClose = nil
		next(services)
	}
}
