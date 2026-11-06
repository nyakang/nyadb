package objectworkspace

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type ObjectSearchRuntime struct {
	// Snapshot is an owned copy of already loaded namespaces, used before searching.
	Snapshot      []contract.Namespace
	Namespaces    func(context.Context) ([]contract.Namespace, error)
	Browse        func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error)
	TableMetadata func(context.Context, contract.ObjectRef) (*contract.TableStructure, error)
}
type ObjectSearchEntry struct {
	Object       contract.DBObject
	Namespace    contract.Namespace
	Field, Match string
}
type objectSearchState struct {
	profile                                int64
	generation                             uint64
	open, busy                             bool
	text, kind, scope, mode, err, selected string
	fields, definitions, caseSensitive     bool
	canFields                              bool
	row                                    int
	list                                   ui.ListState
	cancel                                 context.CancelFunc
	results                                <-chan objectSearchResult
	entries                                []ObjectSearchEntry
	namespaces                             []contract.Namespace
}
type objectSearchResult struct {
	entries    []ObjectSearchEntry
	namespaces []contract.Namespace
	err        error
}

func (s *State) CanSearchObjects(profile int64) bool {
	w := s.sessionFor(profile)
	if w.ProfileID != profile || !w.Connected || !s.capabilities(profile).Metadata || s.deps.SearchRuntime == nil {
		return false
	}
	runtime, err := s.deps.SearchRuntime(profile)
	return err == nil && runtime.Namespaces != nil && runtime.Browse != nil
}
func (s *State) OpenObjectSearch(profile int64) {
	if !s.CanSearchObjects(profile) {
		return
	}
	if s.search == nil || s.search.profile != profile || s.search.generation != s.sessionFor(profile).Generation {
		s.closeObjectSearch()
		s.search = &objectSearchState{profile: profile, generation: s.sessionFor(profile).Generation, kind: "全部", scope: "", mode: "contains", row: -1}
	}
	runtime, _ := s.deps.SearchRuntime(profile)
	s.search.canFields = runtime.TableMetadata != nil
	if !s.search.canFields {
		s.search.fields = false
	}
	if len(s.search.namespaces) == 0 {
		s.search.namespaces = append([]contract.Namespace(nil), runtime.Snapshot...)
	}
	s.search.open = true
}
func (s *State) cancelObjectSearch() {
	if s.search == nil {
		return
	}
	if s.search.cancel != nil {
		s.search.cancel()
	}
	s.search.cancel = nil
	s.search.results = nil
	s.search.busy = false
}
func (s *State) closeObjectSearch() { s.cancelObjectSearch(); s.search = nil }

func objectSearchMatches(text, query, mode string, caseSensitive bool) bool {
	if !caseSensitive {
		text, query = strings.ToLower(text), strings.ToLower(query)
	}
	switch mode {
	case "exact":
		return text == query
	case "prefix":
		return strings.HasPrefix(text, query)
	default:
		return strings.Contains(text, query)
	}
}
func (s *State) startObjectSearch(services ui.Services) {
	x := s.search
	if x == nil || x.busy || !x.open || !s.CanSearchObjects(x.profile) || s.sessionFor(x.profile).Generation != x.generation || strings.TrimSpace(x.text) == "" {
		return
	}
	runtime, err := s.deps.SearchRuntime(x.profile)
	if err != nil || runtime.Namespaces == nil || runtime.Browse == nil {
		x.err = contract.ErrUnsupported.Error()
		return
	}
	request := objectSearchRequest{profile: x.profile, query: strings.TrimSpace(x.text), kind: x.kind, scope: x.scope, mode: x.mode, fields: x.fields, definitions: x.definitions, sensitive: x.caseSensitive}
	if len(request.query) > 4096 {
		x.err = "搜索文本最多 4096 字节"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	x.cancel = cancel
	out := make(chan objectSearchResult, 1)
	x.results, x.busy, x.err = out, true, ""
	x.entries, x.row, x.list = nil, -1, ui.ListState{}
	go func() {
		defer cancel()
		out <- runObjectSearch(ctx, runtime, request)
		services.Invalidate()
	}()
}

type objectSearchRequest struct {
	profile                        int64
	query, kind, scope, mode       string
	fields, definitions, sensitive bool
}

// Limit both display text and owned result memory. Full definitions remain in
// the object snapshot for read-only opening, never in table tooltips.
func objectSearchExcerpt(value, query string, sensitive bool) string {
	text, needle := value, query
	if !sensitive {
		text, needle = strings.ToLower(text), strings.ToLower(needle)
	}
	pos := strings.Index(text, needle)
	// Unicode case folding can change byte offsets; slice only the original runes.
	runes := []rune(value)
	start := 0
	if pos > 0 && pos <= len(text) {
		start = len([]rune(text[:pos])) - 80
	}
	if start < 0 {
		start = 0
	}
	if start > len(runes) {
		start = 0
	}
	end := min(len(runes), start+512)
	out := strings.ReplaceAll(strings.ReplaceAll(string(runes[start:end]), "\n", " "), "\r", " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func runObjectSearch(ctx context.Context, runtime ObjectSearchRuntime, request objectSearchRequest) objectSearchResult {
	result := objectSearchResult{}
	namespaces, err := runtime.Namespaces(ctx)
	result.namespaces = append([]contract.Namespace(nil), namespaces...)
	if err != nil {
		result.err = err
		return result
	}
	issues := []error{}
	issue := func(e error) {
		if len(issues) < 8 {
			issues = append(issues, e)
		}
	}
	used := 0
	limited := false
	for _, namespace := range namespaces {
		if ctx.Err() != nil {
			issue(ctx.Err())
			break
		}
		if namespace.Ref.ProfileID != request.profile || namespace.Label == "" || namespace.Ref.Catalog == "" && namespace.Ref.Schema == "" {
			issue(contract.ErrRevoked)
			continue
		}
		if request.scope != "" && namespace.Label != request.scope {
			continue
		}
		objects, e := runtime.Browse(ctx, namespace.Ref)
		if e != nil {
			issue(e)
			continue
		}
		for _, object := range objects {
			if ctx.Err() != nil {
				issue(ctx.Err())
				break
			}
			kind := searchObjectKind(object.Kind)
			if request.kind != "全部" && request.kind != "" && kind != searchObjectKind(request.kind) {
				continue
			}
			ref := object.Ref
			if ref == (contract.ObjectRef{}) {
				ref = namespace.Ref.Object(kind, object.Name)
			}
			if ref.ProfileID != request.profile || ref.Namespace() != namespace.Ref || ref.Name != object.Name || ref.Name == "" || ref.Kind != kind {
				issue(contract.ErrRevoked)
				continue
			}
			object.Ref = ref
			add := func(field, match string) {
				if limited {
					return
				}
				match = objectSearchExcerpt(match, request.query, request.sensitive)
				cost := len(object.Definition) + len(object.Comment) + len(object.Name) + len(match) + 1024
				if len(result.entries) >= 50000 || used+cost > 16<<20 {
					limited = true
					return
				}
				used += cost
				result.entries = append(result.entries, ObjectSearchEntry{Object: object, Namespace: namespace, Field: field, Match: match})
			}
			matches := func(value string) bool {
				return objectSearchMatches(value, request.query, request.mode, request.sensitive)
			}
			if matches(object.Name) {
				add("名称", object.Name)
			} else if matches(object.Comment) {
				add("注释", object.Comment)
			}
			if request.definitions && object.Definition != "" && matches(object.Definition) {
				add("定义", object.Definition)
			}
			if request.fields && (kind == "table" || kind == "view") && runtime.TableMetadata != nil && !limited {
				metadata, e := runtime.TableMetadata(ctx, ref)
				if e != nil {
					issue(e)
				} else if metadata != nil {
					for _, column := range metadata.Columns {
						if ctx.Err() != nil {
							break
						}
						if matches(column.Name) || matches(column.Comment) {
							add("字段", column.Name+" · "+column.Comment)
						}
						if limited {
							break
						}
					}
				}
			}
			if limited {
				break
			}
		}
		if limited {
			break
		}
	}
	if limited {
		issue(errors.New("搜索结果达到数量或 16 MiB 上限，请缩小范围或搜索文本"))
	}
	if ctx.Err() != nil {
		issue(ctx.Err())
	}
	sort.SliceStable(result.entries, func(i, j int) bool {
		a, b := result.entries[i], result.entries[j]
		if a.Namespace.Label != b.Namespace.Label {
			return a.Namespace.Label < b.Namespace.Label
		}
		if a.Object.Name != b.Object.Name {
			return a.Object.Name < b.Object.Name
		}
		return a.Field < b.Field
	})
	result.err = errors.Join(issues...)
	return result
}

func searchObjectKind(kind string) string {
	switch kind {
	case "包":
		return "package"
	case "触发器":
		return "trigger"
	case "类型":
		return "type"
	}
	switch kind {
	case "键":
		return "keyspace"
	case "表":
		return "table"
	case "视图":
		return "view"
	case "函数":
		return "function"
	case "存储过程":
		return "procedure"
	case "事件":
		return "event"
	case "序列":
		return "sequence"
	default:
		return kind
	}
}
func (s *State) consumeObjectSearch() {
	x := s.search
	if x == nil {
		return
	}
	w := s.sessionFor(x.profile)
	if !w.Connected || w.Generation != x.generation || s.session().ProfileID != x.profile {
		s.cancelObjectSearch()
		x.open = false
		return
	}
	if x.results == nil {
		return
	}
	select {
	case result := <-x.results:
		s.cancelObjectSearch()
		x.entries = result.entries
		x.namespaces = result.namespaces
		x.selected = ""
		x.row = -1
		x.list = ui.ListState{}
		if result.err != nil {
			x.err = result.err.Error()
		}
	default:
	}
}
func (s *State) openSearchEntry(services ui.Services, row int) {
	x := s.search
	if x == nil || !x.open || x.busy || row < 0 || row >= len(x.entries) || !s.CanSearchObjects(x.profile) || s.sessionFor(x.profile).Generation != x.generation || s.session().ProfileID != x.profile || s.deps.OpenSearchObject == nil {
		return
	}
	s.deps.OpenSearchObject(services, x.profile, x.generation, x.entries[row])
	x.open = false
}

func (s *State) clearObjectSearchEntries() {
	if x := s.search; x != nil && !x.busy {
		x.entries = nil
		x.row = -1
		x.selected = ""
		x.list = ui.ListState{}
	}
}
