package objectworkspace

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dictionary"
	"github.com/nyakang/nyadb/locales"
)

type dictionaryScope struct {
	namespace  contract.NamespaceRef
	generation uint64
	database   string
	object     contract.ObjectRef
}
type dictionaryState struct {
	scope                        dictionaryScope
	open, busy                   bool
	err, message, filter, format string
	document                     *dictionary.Document
	row, page                    int
	tables, fields               ui.ListState
	cancel                       context.CancelFunc
	results                      <-chan dictionaryResult
}
type dictionaryResult struct {
	document *dictionary.Document
	path     string
	chosen   bool
	err      error
}

// SetDictionaryFilePicker injects only native file selection. Atomic staging
// and escaping remain in the feature service for every engine.
func (s *State) SetDictionaryFilePicker(save func(string) (string, error)) { s.dictionarySave = save }

func (s *State) dictionaryScope(profile int64, database, table, kind string) (dictionaryScope, bool) {
	w := s.sessionFor(profile)
	b := s.currentBrowser()
	if s.deps.Namespace == nil || s.deps.SearchRuntime == nil || !s.capabilities(profile).Metadata || !w.Connected || w.ProfileID != profile || w.Generation == 0 || s.session().ProfileID != profile || b.ProfileID != profile || b.Database != database || !b.Opened || database == "" {
		return dictionaryScope{}, false
	}
	n := s.deps.Namespace(profile, database)
	if n.ProfileID != profile || n.Catalog == "" && n.Schema == "" {
		return dictionaryScope{}, false
	}
	r, err := s.deps.SearchRuntime(profile)
	if err != nil || r.TableMetadata == nil || table == "" && r.Browse == nil {
		return dictionaryScope{}, false
	}
	scope := dictionaryScope{namespace: n, generation: w.Generation, database: database}
	if table != "" {
		kind = strings.ToLower(searchObjectKind(kind))
		if kind != "table" && kind != "view" {
			return dictionaryScope{}, false
		}
		scope.object = n.Object(kind, table)
	}
	return scope, true
}
func (s *State) dictionaryScopeValid(scope dictionaryScope) bool {
	actual, ok := s.dictionaryScope(scope.namespace.ProfileID, scope.database, scope.object.Name, scope.object.Kind)
	return ok && actual == scope
}
func (s *State) CanDictionary(profile int64, database, table, kind string) bool {
	_, ok := s.dictionaryScope(profile, database, table, kind)
	return ok
}

func (s *State) OpenDictionary(profile int64, database, table, kind string) {
	scope, ok := s.dictionaryScope(profile, database, table, kind)
	if !ok {
		return
	}
	s.closeDictionary()
	s.dictionary = &dictionaryState{scope: scope, open: true, row: -1, format: "markdown"}
}

// DictionaryMenu is rebuilt on selection; retain the opening identity rather
// than silently retargeting a menu after a namespace/session switch.
func (s *State) DictionaryMenu(m *ui.Menu) {
	b := s.currentBrowser()
	current, ready := s.dictionaryScope(b.ProfileID, b.Database, "", "")
	if m.Item(locales.Text("数据字典…")).Disabled(!ready).Chosen() {
		if original := s.dictionaryMenu; original != nil && s.dictionaryScopeValid(*original) {
			s.OpenDictionary(original.namespace.ProfileID, original.database, "", "")
		}
		s.dictionaryMenu = nil
	} else if ready {
		s.dictionaryMenu = &current
	} else {
		s.dictionaryMenu = nil
	}
}

func (s *State) cancelDictionary() {
	if x := s.dictionary; x != nil {
		if x.cancel != nil {
			x.cancel()
		}
		x.cancel = nil
		x.results = nil
		x.busy = false
	}
}
func (s *State) closeDictionary() { s.cancelDictionary(); s.dictionary = nil }

func (s *State) readDictionary(services ui.Services) {
	x := s.dictionary
	if x == nil || !x.open || x.busy || !s.dictionaryScopeValid(x.scope) {
		return
	}
	r, err := s.deps.SearchRuntime(x.scope.namespace.ProfileID)
	if err != nil {
		x.err = err.Error()
		return
	}
	reader := dictionary.Reader{Browse: r.Browse, Metadata: r.TableMetadata}
	namespace, object := x.scope.namespace, x.scope.object
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	out := make(chan dictionaryResult, 1)
	x.cancel, x.results, x.busy, x.err, x.message = cancel, out, true, "", ""
	go func() {
		defer cancel()
		var only *contract.ObjectRef
		if object.Name != "" {
			only = &object
		}
		doc, err := dictionary.Build(ctx, reader, namespace, only)
		reply := dictionaryResult{err: err}
		if err == nil {
			reply.document = &doc
		}
		out <- reply
		services.Invalidate()
	}()
}

func (s *State) chooseDictionaryFile(services ui.Services) {
	x := s.dictionary
	if x == nil || !x.open || x.busy || x.document == nil || s.dictionarySave == nil || !s.dictionaryScopeValid(x.scope) {
		return
	}
	name := "dictionary.md"
	if x.format == "html" {
		name = "dictionary.html"
	}
	save := s.dictionarySave
	out := make(chan dictionaryResult, 1)
	x.results, x.busy, x.err, x.message = out, true, "", ""
	// The callback owns no UI state. Its receipt is checked again on the UI
	// thread before any output write; closing/reconnecting revokes that receipt.
	go func() {
		path, err := save(name)
		out <- dictionaryResult{chosen: true, path: path, err: err}
		services.Invalidate()
	}()
}

func (s *State) exportDictionary(services ui.Services, path string) {
	x := s.dictionary
	if x == nil || !x.open || x.document == nil || path == "" || !s.dictionaryScopeValid(x.scope) {
		return
	}
	doc, format, labels := x.document.Clone(), x.format, dictionaryLabels()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan dictionaryResult, 1)
	x.cancel, x.results, x.busy = cancel, out, true
	go func() {
		defer cancel()
		err := dictionary.Write(ctx, path, doc, format, labels)
		out <- dictionaryResult{path: path, err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeDictionary(services ui.Services) {
	x := s.dictionary
	if x == nil {
		return
	}
	if !x.open || !s.dictionaryScopeValid(x.scope) {
		s.closeDictionary()
		return
	}
	if x.results == nil {
		return
	}
	select {
	case r := <-x.results:
		s.cancelDictionary()
		if r.err != nil {
			x.err = r.err.Error()
			return
		}
		if r.document != nil {
			x.document = r.document
			x.row = 0
			if len(r.document.Tables) == 0 {
				x.row = -1
			}
			x.tables, x.fields = ui.ListState{}, ui.ListState{}
			x.err = ""
			return
		}
		if r.chosen {
			if r.path != "" {
				s.exportDictionary(services, r.path)
			}
			return
		}
		if r.path != "" {
			x.message = locales.Text("字典已导出：") + r.path
		}
	default:
	}
}

func dictionaryLabels() dictionary.Labels {
	return dictionary.Labels{Title: locales.Text("数据字典"), Namespace: locales.Text("数据库/schema"), Name: locales.Text("名称"), Kind: locales.Text("类型"), Engine: locales.Text("引擎"), Comment: locales.Text("注释"), Fields: locales.Text("字段"), Type: locales.Text("字段类型"), Nullable: locales.Text("允许 NULL"), Default: locales.Text("默认值"), Key: locales.Text("键"), Extra: locales.Text("附加属性"), Generated: locales.Text("生成字段"), Indexes: locales.Text("索引"), Unique: locales.Text("唯一"), Columns: locales.Text("字段"), ForeignKeys: locales.Text("外键"), Reference: locales.Text("被引用的对象"), ReferencedColumns: locales.Text("被引用的字段"), OnUpdate: locales.Text("更新时"), OnDelete: locales.Text("删除时"), Yes: locales.Text("是"), No: locales.Text("否"), Unreported: locales.Text("未报告")}
}
