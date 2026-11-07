package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
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
	if name != "*" && !validIndex(name) {
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
			if decodeJSON(raw, &native) != nil || !validIndex(native.Name) || seen[native.Name] {
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
			for _, names := range [][]string{t.Members, t.Aliases} {
				unique := map[string]bool{}
				for _, n := range names {
					if !validIndex(n) || unique[n] {
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

type TargetMetadata struct {
	Target                    IndexTarget
	Mappings, Aliases, Stream json.RawMessage
}

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
				} `json:"indices"`
			} `json:"data_streams"`
		}
		if decodeJSON(stream.Body, &details) != nil || len(details.Streams) != 1 || details.Streams[0].Name != name || len(details.Streams[0].Indices) != len(out.Target.Members) {
			return out, ErrProtocol
		}
		members := map[string]bool{}
		for _, member := range out.Target.Members {
			members[member] = true
		}
		for _, member := range details.Streams[0].Indices {
			if !members[member.Name] {
				return out, ErrProtocol
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
	return out, nil
}
