package solr

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var (
	ErrDataTargetChanged = errors.New("Solr original data target/schema/config changed; reopen the document explicitly")
	ErrDuplicateKey      = errors.New("Solr cursor query contains duplicate keys within a shard; no rows published")
)
var nativeFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)

// A native uniqueKey is unique within one physical shard, not necessarily across
// an alias or a collection with explicit routing. Each traversal unit keeps its
// member/shard identity. The duplicate-key facet checks the current query scope.
type readUnit struct{ Member, Shard, Key string }
type dataBinding struct {
	Metadata TargetMetadata
	Units    []readUnit
}

func parseDataBinding(m TargetMetadata) (*dataBinding, error) {
	b := &dataBinding{Metadata: m}
	states := map[string]json.RawMessage{}
	if m.Target.Kind == "collection" {
		var v struct{ State json.RawMessage }
		if decodeJSON(m.Target.Definition, &v) != nil {
			return nil, ErrProtocol
		}
		states[m.Target.Name] = v.State
	} else if m.Target.Kind == "alias" {
		var v struct {
			Collections []struct{ State json.RawMessage }
		}
		if decodeJSON(m.Target.Definition, &v) != nil || len(v.Collections) != len(m.Target.Members) {
			return nil, ErrProtocol
		}
		for i, name := range m.Target.Members {
			states[name] = v.Collections[i].State
		}
	}
	for _, member := range m.Members {
		var schema struct {
			UniqueKey string `json:"uniqueKey"`
			Fields    []struct {
				Name, Type  string
				MultiValued bool
			}
		}
		if decodeJSON(member.Schema, &schema) != nil || !nativeFieldName.MatchString(schema.UniqueKey) {
			return nil, contract.ErrUnsupported
		}
		found := false
		for _, field := range schema.Fields {
			if field.Name == schema.UniqueKey && !field.MultiValued && field.Type != "" {
				found = true
			}
		}
		if !found {
			return nil, contract.ErrUnsupported
		}
		if err := validateSelectConfig(member.Config); err != nil {
			return nil, err
		}
		if m.Target.Kind == "core" {
			b.Units = append(b.Units, readUnit{Member: member.Name, Key: schema.UniqueKey})
			continue
		}
		var state struct {
			Shards map[string]struct {
				State    string
				Replicas map[string]struct{ State string }
			}
		}
		if decodeJSON(states[member.Name], &state) != nil || len(state.Shards) == 0 {
			return nil, ErrProtocol
		}
		shards := make([]string, 0, len(state.Shards))
		for name, shard := range state.Shards {
			if shard.State == "inactive" {
				continue
			}
			if !validTargetName(name) || shard.State != "active" {
				return nil, contract.ErrUnsupported
			}
			active := false
			for _, replica := range shard.Replicas {
				if replica.State == "active" {
					active = true
				}
			}
			if !active {
				return nil, contract.ErrUnsupported
			}
			shards = append(shards, name)
		}
		sort.Strings(shards)
		for _, shard := range shards {
			b.Units = append(b.Units, readUnit{Member: member.Name, Shard: shard, Key: schema.UniqueKey})
		}
		if len(b.Units) > 4096 {
			return nil, ErrBounds
		}
	}
	if len(b.Units) == 0 {
		return nil, contract.ErrUnsupported
	}
	return b, nil
}
func validateSelectConfig(raw []byte) error {
	var config struct {
		Handlers   map[string]map[string]json.RawMessage `json:"requestHandler"`
		InitParams []map[string]json.RawMessage          `json:"initParams"`
		Components map[string]struct{ Class string }     `json:"searchComponent"`
	}
	if decodeJSON(raw, &config) != nil {
		return ErrProtocol
	}
	handler := config.Handlers["/select"]
	var class string
	if decodeJSON(handler["class"], &class) != nil || class != "solr.SearchHandler" && class != "org.apache.solr.handler.component.SearchHandler" {
		return contract.ErrUnsupported
	}
	check := func(params map[string]json.RawMessage) error {
		for _, key := range []string{"invariants", "appends", "useParams", "components", "first-components", "last-components"} {
			if value, ok := params[key]; ok && string(value) != "null" && string(value) != "{}" && string(value) != "[]" {
				return contract.ErrUnsupported
			}
		}
		if raw, ok := params["defaults"]; ok && string(raw) != "null" {
			var defaults map[string]json.RawMessage
			if decodeJSON(raw, &defaults) != nil {
				return ErrProtocol
			}
			for key := range defaults {
				// These defaults are explicitly overridden, or df is irrelevant to
				// our match-all and explicit-field term grammar. Inherited fq,
				// parsers and other parameters can silently narrow a full export.
				switch key {
				case "rows", "echoParams", "shards", "distrib", "df":
				default:
					return contract.ErrUnsupported
				}
			}
		}
		return nil
	}
	if err := check(handler); err != nil {
		return err
	}
	for _, params := range config.InitParams {
		if err := check(params); err != nil {
			return err
		}
	}
	for name, classes := range map[string][]string{
		"query":        {"solr.QueryComponent", "org.apache.solr.handler.component.QueryComponent"},
		"facet":        {"solr.FacetComponent", "org.apache.solr.handler.component.FacetComponent"},
		"facet_module": {"solr.FacetModule", "org.apache.solr.search.facet.FacetModule"},
	} {
		if component, ok := config.Components[name]; ok && component.Class != classes[0] && component.Class != classes[1] {
			return contract.ErrUnsupported
		}
	}

	return nil
}
func metadataFingerprint(t Target, members []MemberMetadata) string {
	raw, _ := json.Marshal(struct {
		Target  Target
		Members []MemberMetadata
	}{t, members})
	return fingerprint(raw)
}
func (l *dataLease) guard(ctx context.Context, c *Client) (*dataBinding, error) {
	l.mu.Lock()
	changed := l.changed
	l.mu.Unlock()
	if changed {
		return nil, ErrDataTargetChanged
	}
	if err := l.session.validateNode(ctx, c); err != nil {
		return nil, err
	}
	targets, err := c.ResolveTargets(ctx)
	if err != nil {
		return nil, err
	}
	t, err := findTarget(targets, l.object.Name, l.object.Kind)
	if err != nil {
		return nil, l.targetChanged()
	}
	members, err := c.readMembers(ctx, t)
	if err != nil {
		return nil, err
	}
	again, err := c.ResolveTargets(ctx)
	if err != nil {
		return nil, err
	}
	observed, err := findTarget(again, l.object.Name, l.object.Kind)
	if err != nil || observed.Fingerprint != t.Fingerprint {
		return nil, l.targetChanged()
	}
	if err = l.session.validateNode(ctx, c); err != nil {
		return nil, err
	}
	actual := TargetMetadata{Target: t, Members: members, Fingerprint: metadataFingerprint(t, members)}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, contract.ErrRevoked
	}
	if l.changed {
		return nil, ErrDataTargetChanged
	}
	if l.binding == nil {
		l.binding, err = parseDataBinding(actual)
		if err != nil {
			return nil, err
		}
	} else if l.binding.Metadata.Fingerprint != actual.Fingerprint {
		l.changed = true
		return nil, ErrDataTargetChanged
	}
	return l.binding, ctx.Err()
}
func (l *dataLease) targetChanged() error {
	l.mu.Lock()
	l.changed = true
	l.mu.Unlock()
	return ErrDataTargetChanged
}
