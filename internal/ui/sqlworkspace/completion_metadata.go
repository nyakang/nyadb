package sqlworkspace

import (
	"context"
	"encoding/json"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"time"
)

// Bound on the UI thread; the captured loader owns the original session.
type CompletionSource struct {
	Generation uint64
	Catalog    string
	Load       func(context.Context, string, string) ([]string, error)
}
type completionMetadata struct {
	key        string
	profile    int64
	generation uint64
	catalog    string
	refs       []sqltext.CompletionObject
	due        time.Time
	cancel     context.CancelFunc
	timer      *time.Timer
	out        <-chan completionReply
	request    *editor.CompletionRequest
}
type completionReply struct {
	key     string
	objects []sqltext.CompletionObject
}

func completionObjectKey(profile int64, generation uint64, catalog string, object sqltext.CompletionObject) string {
	b, _ := json.Marshal(struct {
		Profile                  int64
		Generation               uint64
		Catalog, Namespace, Name string
	}{profile, generation, catalog, object.Namespace, object.Name})
	return string(b)
}

func (s *State) completionCatalog(d *Document) sqltext.CompletionCatalog {
	profile, ns := d.sourceScope()
	c := s.deps.CompletionCatalog(profile, ns)
	if s.completionCache == nil {
		s.completionCache = map[string][]sqltext.CompletionObject{}
	}
	for _, obj := range s.completionCache[d.metadata.key] {
		found := false
		for i := range c.Objects {
			if c.Objects[i].Namespace == obj.Namespace && c.Objects[i].Name == obj.Name {
				c.Objects[i].Columns = append([]string(nil), obj.Columns...)
				found = true
				break
			}
		}
		if !found {
			c.Objects = append(c.Objects, obj)
		}
	}
	return c
}
func (s *State) updateCompletionMetadata(d *Document) {
	if d.cypher() {
		d.metadata.stop()
		return
	}
	if s.deps.CompletionSource == nil || s.deps.CompletionCatalog == nil || d.closed || d.recoveryNeeded || !s.connected(d.profileID) {
		d.metadata.stop()
		return
	}
	source := s.deps.CompletionSource(d.profileID)
	if source.Load == nil || source.Generation == 0 {
		d.metadata.stop()
		return
	}
	catalog := s.deps.CompletionCatalog(d.profileID, d.database)
	refs := sqltext.ReferencedObjects(d.Editor.Buffer.String(), catalog.Dialect, d.database)
	b, _ := json.Marshal(struct {
		Profile    int64
		Generation uint64
		Catalog    string
		Refs       []sqltext.CompletionObject
	}{d.profileID, source.Generation, source.Catalog, refs})
	key := string(b)
	m := &d.metadata
	if key != m.key {
		m.stop()
		m.key, m.profile, m.generation, m.refs, m.due = key, d.profileID, source.Generation, refs, time.Now().Add(250*time.Millisecond)
		m.catalog = source.Catalog
		if s.Invalidate != nil {
			m.timer = time.AfterFunc(250*time.Millisecond, s.Invalidate)
		}
	}
	if len(refs) == 0 || m.out != nil || time.Now().Before(m.due) {
		return
	}
	if _, cached := s.completionCache[key]; cached {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	out := make(chan completionReply, 1)
	m.out = out
	profile := d.profileID
	for i, ref := range refs {
		if obj, ok := s.completionObjects[completionObjectKey(profile, source.Generation, source.Catalog, ref)]; ok {
			refs[i].Columns = append([]string{}, obj.Columns...)
		}
	}
	invalidate := s.Invalidate
	go func() {
		objects := []sqltext.CompletionObject{}
		for _, ref := range refs {
			if ctx.Err() != nil {
				break
			}
			columns, err := ref.Columns, error(nil)
			if columns == nil {
				columns, err = source.Load(ctx, ref.Namespace, ref.Name)
			}
			if err == nil {
				ref.Columns = append([]string(nil), columns...)
				objects = append(objects, ref)
			}
		}
		out <- completionReply{key, objects}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (m *completionMetadata) stop() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.out = nil
}
func (s *State) consumeCompletionMetadata(d *Document) {
	s.updateCompletionMetadata(d)
	m := &d.metadata
	if m.out == nil {
		return
	}
	select {
	case reply := <-m.out:
		m.out = nil
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		if reply.key != m.key || d.closed || d.recoveryNeeded {
			return
		}
		if s.completionCache == nil {
			s.completionCache = map[string][]sqltext.CompletionObject{}
		}
		if len(s.completionCache) > 128 {
			clear(s.completionCache)
		}
		s.completionCache[reply.key] = reply.objects
		if s.completionObjects == nil {
			s.completionObjects = map[string]sqltext.CompletionObject{}
		}
		if len(s.completionObjects) > 128 {
			clear(s.completionObjects)
		}
		for _, object := range reply.objects {
			copy := object
			copy.Columns = append([]string{}, object.Columns...)
			s.completionObjects[completionObjectKey(m.profile, m.generation, m.catalog, object)] = copy
		}
		if m.request != nil && s.active(d) && m.request.Version == d.Editor.Buffer.Version() {
			request := *m.request
			m.request = nil
			s.completions(d, request)
		}
	default:
	}
}
