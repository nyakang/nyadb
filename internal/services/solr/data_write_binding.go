package solr

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type effectiveField struct {
	Name, Type                                        string
	Stored, Indexed, DocValues, MultiValued, Required *bool
	Default                                           json.RawMessage
}
type writeMember struct {
	chain, router, routeField string
	fields                    map[string]effectiveField
	dynamic                   []effectiveField
	copies                    map[string]bool
	ranges                    map[string]shardRange
}
type writeBinding struct {
	signature string
	eligible  bool
	members   map[string]*writeMember
}

// Each observation retains effective field defaults and the separate params
// store. The latter can be absent (native znodeVersion=-1, no params property).
// It must not be confused with a fabricated empty parameter set.
func (l *dataLease) writeGuard(ctx context.Context, c *Client) (*writeBinding, error) {
	b, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	w := &writeBinding{eligible: true, members: map[string]*writeMember{}}
	// Sparse metadata can conclusively reject nested/copy schemas. Such targets
	// retain the original read-only binding without extra eligibility requests.
	for _, member := range b.Metadata.Members {
		var schema struct {
			Fields     []struct{ Name string }
			CopyFields []json.RawMessage
		}
		if decodeJSON(member.Schema, &schema) != nil {
			return nil, ErrProtocol
		}
		if len(schema.CopyFields) > 0 || slices.ContainsFunc(schema.Fields, func(f struct{ Name string }) bool { return f.Name == "_root_" || f.Name == "_nest_path_" }) {
			w.eligible = false
			w.signature = b.Metadata.Fingerprint
			return l.publishWriteBinding(w, ctx)
		}
	}
	observations := map[string][]json.RawMessage{}
	for _, m := range b.Metadata.Members {
		values := make([]json.RawMessage, 0, 3)
		for _, path := range []string{"/schema/fields?wt=json&showDefaults=true", "/schema/dynamicfields?wt=json&showDefaults=true", "/config/params?wt=json"} {
			var envelope map[string]json.RawMessage
			if err = c.nativeJSON(ctx, "/"+url.PathEscape(m.Name)+path, &envelope); err != nil {
				return nil, err
			}
			delete(envelope, "responseHeader")
			raw, _ := json.Marshal(envelope)
			values = append(values, raw)
		}
		observations[m.Name] = values
		member, e := qualifyWriteMember(m, values)
		if e != nil {
			if !errors.Is(e, contract.ErrUnsupported) {
				return nil, e
			}
			w.eligible = false
		} else {
			w.members[m.Name] = member
		}
	}
	if w.eligible {
		if err = bindWriteRoutes(b, w); err != nil {
			if !errors.Is(err, contract.ErrUnsupported) {
				return nil, err
			}
			w.eligible = false
		}
	}
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(struct {
		Base         string
		Observations map[string][]json.RawMessage
	}{b.Metadata.Fingerprint, observations})
	w.signature = fingerprint(raw)
	return l.publishWriteBinding(w, ctx)
}
func (l *dataLease) publishWriteBinding(w *writeBinding, ctx context.Context) (*writeBinding, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, contract.ErrRevoked
	}
	if l.changed {
		return nil, ErrDataTargetChanged
	}
	if l.writeBinding == nil {
		l.writeBinding = w
	} else if l.writeBinding.signature != w.signature {
		l.changed = true
		return nil, ErrDataTargetChanged
	}
	return l.writeBinding, ctx.Err()
}

func qualifyWriteMember(m MemberMetadata, observations []json.RawMessage) (*writeMember, error) {
	if len(observations) != 3 || slices.ContainsFunc(observations, func(v json.RawMessage) bool { return len(v) == 0 }) {
		return nil, contract.ErrUnsupported
	}
	var fields struct{ Fields []effectiveField }
	var dynamic struct{ DynamicFields []effectiveField }
	var params struct{ Response map[string]json.RawMessage }
	if decodeJSON(observations[0], &fields) != nil || fields.Fields == nil || decodeJSON(observations[1], &dynamic) != nil || dynamic.DynamicFields == nil || decodeJSON(observations[2], &params) != nil || params.Response == nil {
		return nil, ErrProtocol
	}
	if raw, ok := params.Response["params"]; ok {
		var sets map[string]json.RawMessage
		if decodeJSON(raw, &sets) != nil || sets == nil {
			return nil, ErrProtocol
		}
		if len(sets) != 0 {
			return nil, contract.ErrUnsupported
		}
	} else {
		var version int
		if decodeJSON(params.Response["znodeVersion"], &version) != nil || version != -1 {
			return nil, ErrProtocol
		}
	}
	var schema struct {
		UniqueKey  string
		FieldTypes []struct{ Name, Class string }
		CopyFields []struct{ Source, Dest string }
	}
	if decodeJSON(m.Schema, &schema) != nil {
		return nil, ErrProtocol
	}
	// A non-stored copy destination can also contain independent historical
	// input. Its provenance is not observable; this first qualified tier rejects
	// all copy rules rather than dropping that input during reconstruction.
	if len(schema.CopyFields) != 0 {
		return nil, contract.ErrUnsupported
	}
	out := &writeMember{fields: map[string]effectiveField{}, copies: map[string]bool{}, ranges: map[string]shardRange{}}
	types := map[string]string{}
	for _, t := range schema.FieldTypes {
		types[t.Name] = t.Class
	}
	for _, copy := range schema.CopyFields {
		if !nativeFieldName.MatchString(copy.Source) || !nativeFieldName.MatchString(copy.Dest) || copy.Dest == schema.UniqueKey {
			return nil, contract.ErrUnsupported
		}
		out.copies[copy.Dest] = true
	}
	all := append(append([]effectiveField(nil), fields.Fields...), dynamic.DynamicFields...)
	for i, f := range all {
		if f.Stored == nil || f.Indexed == nil || f.DocValues == nil || f.MultiValued == nil || f.Required == nil || f.Name == "_root_" || f.Name == "_nest_path_" {
			return nil, contract.ErrUnsupported
		}
		if i < len(fields.Fields) {
			if !nativeFieldName.MatchString(f.Name) || out.fields[f.Name].Name != "" {
				return nil, contract.ErrUnsupported
			}
			out.fields[f.Name] = f
		} else {
			if strings.Count(f.Name, "*") != 1 || !strings.HasPrefix(f.Name, "*") && !strings.HasSuffix(f.Name, "*") {
				return nil, contract.ErrUnsupported
			}
			out.dynamic = append(out.dynamic, f)
		}
		if f.Name == "_version_" {
			if *f.MultiValued || !*f.DocValues || types[f.Type] != "solr.LongPointField" {
				return nil, contract.ErrUnsupported
			}
			continue
		}
		if out.copies[f.Name] {
			// Solr's atomic reconstruction regenerates copy destinations. Stored or
			// docValues copy destinations may have independent input we cannot recover.
			if *f.Stored || *f.DocValues {
				return nil, contract.ErrUnsupported
			}
			continue
		}
		if !*f.Stored {
			return nil, contract.ErrUnsupported
		}
		switch types[f.Type] {
		case "solr.StrField", "solr.TextField", "solr.BoolField", "solr.IntPointField", "solr.LongPointField", "solr.FloatPointField", "solr.DoublePointField", "solr.DatePointField":
		default:
			return nil, contract.ErrUnsupported
		}
	}
	key, ok := out.fields[schema.UniqueKey]
	if !ok || *key.MultiValued || !*key.Stored || !*key.Indexed || types[key.Type] != "solr.StrField" || out.fields["_version_"].Name == "" {
		return nil, contract.ErrUnsupported
	}
	// Dynamic resolution is longest match, stable for equal lengths in native
	// declaration order. Every prototype has already passed recovery qualification.
	slices.SortStableFunc(out.dynamic, func(a, b effectiveField) int { return len(b.Name) - len(a.Name) })
	chain, err := safeWriteChain(m.Config)
	if err != nil {
		return nil, err
	}
	out.chain = chain
	return out, nil
}

func safeWriteChain(raw []byte) (string, error) {
	var config struct {
		RequestHandler              map[string]map[string]json.RawMessage
		InitParams                  []map[string]json.RawMessage
		UpdateRequestProcessorChain []map[string]json.RawMessage
	}
	if decodeJSON(raw, &config) != nil {
		return "", ErrProtocol
	}
	check := func(p map[string]json.RawMessage, expected string) error {
		for k, v := range p {
			switch k {
			case "name", "startup":
			case "class":
				var s string
				if decodeJSON(v, &s) != nil || s != expected {
					return contract.ErrUnsupported
				}
			case "useParams":
				var s string
				if decodeJSON(v, &s) != nil || s != "_GET" && s != "_UPDATE" {
					return contract.ErrUnsupported
				}
			case "path":
			case "defaults":
				var defaults map[string]json.RawMessage
				if decodeJSON(v, &defaults) != nil {
					return ErrProtocol
				}
				for name := range defaults {
					switch name {
					case "omitHeader", "echoParams", "wt", "df":
					default:
						return contract.ErrUnsupported
					}
				}
			default:
				return contract.ErrUnsupported
			}
		}
		return nil
	}
	for path, class := range map[string]string{"/get": "solr.RealTimeGetHandler", "/update": "solr.UpdateRequestHandler"} {
		h := config.RequestHandler[path]
		if h == nil {
			return "", contract.ErrUnsupported
		}
		var actual string
		if decodeJSON(h["class"], &actual) != nil || actual != class {
			return "", contract.ErrUnsupported
		}
		if err := check(h, class); err != nil {
			return "", err
		}
	}
	for _, p := range config.InitParams {
		if err := check(p, ""); err != nil {
			return "", err
		}
	}
	names := []string{}
	for _, chain := range config.UpdateRequestProcessorChain {
		var name string
		if decodeJSON(chain["name"], &name) != nil || !nativeFieldName.MatchString(name) {
			continue
		}
		safe := true
		for k := range chain {
			if k != "name" && k != "default" && k != "" {
				safe = false
			}
		}
		var processors []map[string]json.RawMessage
		if decodeJSON(chain[""], &processors) != nil || len(processors) != 3 {
			continue
		}
		for i, class := range []string{"solr.LogUpdateProcessorFactory", "solr.DistributedUpdateProcessorFactory", "solr.RunUpdateProcessorFactory"} {
			var actual string
			if len(processors[i]) != 1 || decodeJSON(processors[i]["class"], &actual) != nil || actual != class {
				safe = false
			}
		}
		if safe {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", contract.ErrUnsupported
	}
	sort.Strings(names)
	return names[0], nil
}

func bindWriteRoutes(b *dataBinding, w *writeBinding) error {
	states := map[string]json.RawMessage{}
	if b.Metadata.Target.Kind == "core" {
		return nil
	}
	if b.Metadata.Target.Kind == "alias" {
		var alias struct {
			Properties  map[string]json.RawMessage
			Collections []struct{ State json.RawMessage }
		}
		if decodeJSON(b.Metadata.Target.Definition, &alias) != nil || len(alias.Collections) != len(b.Metadata.Members) {
			return ErrProtocol
		}
		if len(alias.Properties) != 0 {
			return contract.ErrUnsupported
		}
		for i, m := range b.Metadata.Members {
			states[m.Name] = alias.Collections[i].State
		}
	} else {
		var target struct{ State json.RawMessage }
		if decodeJSON(b.Metadata.Target.Definition, &target) != nil {
			return ErrProtocol
		}
		states[b.Metadata.Target.Name] = target.State
	}
	for name, member := range w.members {
		var state struct {
			Router struct{ Name, Field string }
			Shards map[string]struct{ State, Range string }
		}
		if decodeJSON(states[name], &state) != nil {
			return ErrProtocol
		}
		if state.Router.Name != "implicit" && state.Router.Name != "compositeId" {
			return contract.ErrUnsupported
		}
		member.router, member.routeField = state.Router.Name, state.Router.Field
		if member.routeField != "" {
			for _, unit := range b.Units {
				if unit.Member == name && unit.Key == member.routeField {
					return contract.ErrUnsupported
				}
			}
			f, ok := member.fields[member.routeField]
			if !ok || *f.MultiValued || !*f.Stored || member.copies[member.routeField] {
				return contract.ErrUnsupported
			}
		}
		for shard, s := range state.Shards {
			if s.State != "active" {
				continue
			}
			if member.router == "implicit" {
				member.ranges[shard] = shardRange{}
				continue
			}
			r, err := parseShardRange(s.Range)
			if err != nil {
				return err
			}
			member.ranges[shard] = r
		}
		if member.router == "compositeId" {
			ranges := make([]shardRange, 0, len(member.ranges))
			for _, r := range member.ranges {
				ranges = append(ranges, r)
			}
			slices.SortFunc(ranges, func(a, b shardRange) int {
				if a.min < b.min {
					return -1
				}
				if a.min > b.min {
					return 1
				}
				return 0
			})
			if len(ranges) == 0 || ranges[0].min != -1<<31 || ranges[len(ranges)-1].max != 1<<31-1 {
				return contract.ErrUnsupported
			}
			for i := 1; i < len(ranges); i++ {
				if int64(ranges[i-1].max)+1 != int64(ranges[i].min) {
					return contract.ErrUnsupported
				}
			}
		}
	}
	return nil
}
func (m *writeMember) field(name string) (effectiveField, bool) {
	if f, ok := m.fields[name]; ok {
		return f, true
	}
	for _, f := range m.dynamic {
		parts := strings.Split(f.Name, "*")
		if strings.HasPrefix(name, parts[0]) && strings.HasSuffix(name, parts[1]) && len(name) >= len(f.Name)-1 {
			return f, true
		}
	}
	return effectiveField{}, false
}
func (m *writeMember) nativeRef(unit readUnit, key contract.Cell, fields map[string]json.RawMessage) (NativeDocumentRef, error) {
	ref := NativeDocumentRef{Member: unit.Member, UniqueKey: unit.Key, Key: key, Shard: unit.Shard, Router: m.router, RouteField: m.routeField}
	if m.routeField != "" {
		var err error
		ref.RouteValue, err = keyCell(fields[m.routeField])
		if err != nil {
			return ref, contract.ErrUnsupported
		}
	}
	if m.router == "implicit" {
		if _, ok := m.ranges[unit.Shard]; !ok || m.routeField != "" && ref.RouteValue.Text != unit.Shard {
			return ref, contract.ErrDataConflict
		}
	} else if m.router == "compositeId" {
		input := key.Text
		if m.routeField != "" {
			input = ref.RouteValue.Text
		}
		hash, err := compositeHash(input)
		if err != nil {
			return ref, err
		}
		shard := ""
		for name, r := range m.ranges {
			if hash >= r.min && hash <= r.max {
				if shard != "" {
					return ref, contract.ErrUnsupported
				}
				shard = name
			}
		}
		if shard == "" || shard != unit.Shard {
			return ref, contract.ErrDataConflict
		}
	}
	return ref, nil
}
