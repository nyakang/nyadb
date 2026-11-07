package easysearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
)

// IndexTarget retains the native distinction between concrete indices, aliases
// and data streams, including the exact resolution entry and concrete members.
type IndexTarget struct {
	Name, Kind                   string
	Attributes, Aliases, Members []string
	Definition                   json.RawMessage
}

func (c *Client) ResolveTargets(ctx context.Context, name string) ([]IndexTarget, error) {
	if name != "*" && !validTargetSelector(name) {
		return nil, ErrInvalid
	}
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/_resolve/index/" + url.PathEscape(name) + "?expand_wildcards=all"})
	if err != nil {
		return nil, err
	}
	var root struct {
		Indices []json.RawMessage `json:"indices"`
		Aliases []json.RawMessage `json:"aliases"`
		Streams []json.RawMessage `json:"data_streams"`
	}
	if decodeJSON(r.Body, &root) != nil || root.Indices == nil || root.Aliases == nil || root.Streams == nil || len(root.Indices)+len(root.Aliases)+len(root.Streams) > 16384 {
		return nil, ErrProtocol
	}
	out := make([]IndexTarget, 0, len(root.Indices)+len(root.Aliases)+len(root.Streams))
	seen := map[string]bool{}
	for _, group := range []struct {
		kind    string
		entries []json.RawMessage
	}{{"index", root.Indices}, {"alias", root.Aliases}, {"data_stream", root.Streams}} {
		for _, raw := range group.entries {
			var native struct {
				Name       string   `json:"name"`
				Attributes []string `json:"attributes"`
				Aliases    []string `json:"aliases"`
				Indices    []string `json:"indices"`
				Backing    []string `json:"backing_indices"`
			}
			if decodeJSON(raw, &native) != nil || !validTargetName(group.kind, native.Name) || seen[native.Name] {
				return nil, ErrProtocol
			}
			seen[native.Name] = true
			t := IndexTarget{Name: native.Name, Kind: group.kind, Attributes: append([]string(nil), native.Attributes...), Aliases: append([]string(nil), native.Aliases...), Definition: bytes.Clone(raw)}
			switch group.kind {
			case "index":
				t.Members = []string{t.Name}
			case "alias":
				t.Members = append([]string(nil), native.Indices...)
			case "data_stream":
				t.Members = append([]string(nil), native.Backing...)
			}
			if len(t.Members) == 0 || len(t.Members) > 16384 || len(t.Aliases) > 16384 {
				return nil, ErrProtocol
			}
			for group, names := range [][]string{t.Members, t.Aliases} {
				unique := map[string]bool{}
				for _, n := range names {
					valid := validIndex(n)
					if group == 1 {
						valid = validTargetSelector(n)
					}
					if !valid || unique[n] {
						return nil, ErrProtocol
					}
					unique[n] = true
				}
			}
			for _, attr := range t.Attributes {
				if attr == "" || len(attr) > 128 || strings.ContainsAny(attr, "\r\n\x00") {
					return nil, ErrProtocol
				}
			}
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Alias selectors preserve case. Concrete indices and data streams still use
// their native lowercase rules. Validation never rewrites object identity.
func validTargetSelector(name string) bool {
	return len(name) <= 255 && validIndex(strings.ToLower(name))
}

func validTargetName(kind, name string) bool {
	if kind == "alias" {
		return validTargetSelector(name)
	}
	return validIndex(name)
}

type TargetMetadata struct {
	Target                              IndexTarget
	Mappings, Aliases, Stream, Settings json.RawMessage
	IndexUUIDs                          map[string]string
}

var ErrTargetChanged = errors.New("Easysearch target changed while reading metadata; refresh explicitly")

func (c *Client) ReadTargetMetadata(ctx context.Context, name, kind string) (TargetMetadata, error) {
	var out TargetMetadata
	if kind != "index" && kind != "alias" && kind != "data_stream" {
		return out, ErrInvalid
	}
	targets, err := c.ResolveTargets(ctx, name)
	if err != nil {
		return out, err
	}
	found := false
	for _, target := range targets {
		if target.Name == name && target.Kind == kind {
			out.Target = target
			found = true
			break
		}
	}
	if !found {
		return out, ErrInvalid
	}
	settings, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(name) + "/_settings?flat_settings=true&expand_wildcards=all"})
	if err != nil {
		return out, err
	}
	out.IndexUUIDs, err = metadataIndexUUIDs(settings.Body, out.Target.Members)
	if err != nil {
		return out, err
	}
	out.Settings = bytes.Clone(settings.Body)
	mapping, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(name) + "/_mapping"})
	if err != nil {
		return out, err
	}
	var mapped map[string]json.RawMessage
	if decodeJSON(mapping.Body, &mapped) != nil || len(mapped) != len(out.Target.Members) {
		return out, ErrProtocol
	}
	for _, member := range out.Target.Members {
		if mapped[member] == nil {
			return out, ErrProtocol
		}
	}
	out.Mappings = bytes.Clone(mapping.Body)
	if kind == "data_stream" {
		stream, err := c.Do(ctx, Request{Method: "GET", Path: "/_data_stream/" + url.PathEscape(name)})
		if err != nil {
			return out, err
		}
		var details struct {
			Streams []struct {
				Name    string `json:"name"`
				Indices []struct {
					Name string `json:"index_name"`
					UUID string `json:"index_uuid"`
				} `json:"indices"`
			} `json:"data_streams"`
		}
		if decodeJSON(stream.Body, &details) != nil || len(details.Streams) != 1 || details.Streams[0].Name != name || details.Streams[0].Indices == nil {
			return out, ErrProtocol
		}
		if len(details.Streams[0].Indices) != len(out.Target.Members) {
			return out, ErrTargetChanged
		}
		members := map[string]bool{}
		for _, member := range out.Target.Members {
			members[member] = true
		}
		for _, member := range details.Streams[0].Indices {
			if !validIndex(member.Name) || !validPIT(member.UUID) || len(member.UUID) > 128 {
				return out, ErrProtocol
			}
			if !members[member.Name] || out.IndexUUIDs[member.Name] != member.UUID {
				return out, ErrTargetChanged
			}
			delete(members, member.Name)
		}
		if len(members) != 0 {
			return out, ErrProtocol
		}
		out.Stream = bytes.Clone(stream.Body)
	} else {
		path := "/" + url.PathEscape(name) + "/_alias"
		if kind == "alias" {
			path = "/_alias/" + url.PathEscape(name)
		}
		alias, err := c.Do(ctx, Request{Method: "GET", Path: path})
		if err != nil {
			return out, err
		}
		var entries map[string]json.RawMessage
		if decodeJSON(alias.Body, &entries) != nil || len(entries) != len(out.Target.Members) {
			return out, ErrProtocol
		}
		for _, member := range out.Target.Members {
			var detail struct {
				Aliases map[string]json.RawMessage `json:"aliases"`
			}
			if entries[member] == nil || decodeJSON(entries[member], &detail) != nil || detail.Aliases == nil || kind == "alias" && detail.Aliases[name] == nil {
				return out, ErrProtocol
			}
		}
		out.Aliases = bytes.Clone(alias.Body)
	}
	// Metadata APIs do not provide a catalog transaction. Check the same
	// concrete identities and alias/stream definition again before publishing;
	// detected replacement or rollover is an explicit refresh, never success.
	after, err := c.ResolveTargets(ctx, name)
	if err != nil {
		return out, err
	}
	stable := false
	for _, target := range after {
		if sameTargetResolution(out.Target, target) {
			stable = true
		}
	}
	if !stable {
		return out, ErrTargetChanged
	}
	check, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(name) + "/_settings/index.uuid?flat_settings=true&expand_wildcards=all"})
	if err != nil {
		return out, err
	}
	uuids, err := metadataIndexUUIDs(check.Body, out.Target.Members)
	if err != nil {
		return out, err
	}
	if !sameIndexUUIDs(out.IndexUUIDs, uuids) {
		return out, ErrTargetChanged
	}
	path, definition := "/"+url.PathEscape(name)+"/_alias", out.Aliases
	if kind == "alias" {
		path = "/_alias/" + url.PathEscape(name)
	} else if kind == "data_stream" {
		path, definition = "/_data_stream/"+url.PathEscape(name), out.Stream
	}
	check, err = c.Do(ctx, Request{Method: "GET", Path: path})
	if err != nil {
		return out, err
	}
	if !sameNativeJSON(definition, check.Body) {
		return out, ErrTargetChanged
	}
	return out, nil
}

func metadataIndexUUIDs(raw []byte, members []string) (map[string]string, error) {
	var indices map[string]struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if decodeJSON(raw, &indices) != nil || len(indices) != len(members) || len(members) == 0 {
		return nil, ErrProtocol
	}
	out := make(map[string]string, len(members))
	for _, member := range members {
		var uuid string
		if !validIndex(member) || json.Unmarshal(indices[member].Settings["index.uuid"], &uuid) != nil || !validPIT(uuid) || len(uuid) > 128 || out[member] != "" {
			return nil, ErrProtocol
		}
		out[member] = uuid
	}
	return out, nil
}

func sameIndexUUIDs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, uuid := range a {
		if uuid != b[name] {
			return false
		}
	}
	return true
}

func sameTargetResolution(a, b IndexTarget) bool {
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		left, right := append([]string(nil), a...), append([]string(nil), b...)
		sort.Strings(left)
		sort.Strings(right)
		for i := range left {
			if left[i] != right[i] {
				return false
			}
		}
		return true
	}
	return a.Name == b.Name && a.Kind == b.Kind && equal(a.Members, b.Members) && equal(a.Attributes, b.Attributes) && equal(a.Aliases, b.Aliases)
}

func sameNativeJSON(a, b []byte) bool {
	canonical := func(raw []byte) []byte {
		var valid json.RawMessage
		if decodeJSON(raw, &valid) != nil {
			return nil
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var value any
		if d.Decode(&value) != nil {
			return nil
		}
		out, _ := json.Marshal(value)
		return out
	}
	a, b = canonical(a), canonical(b)
	return a != nil && b != nil && bytes.Equal(a, b)
}
