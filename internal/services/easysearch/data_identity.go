package easysearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var ErrPITWindow = fmt.Errorf("%w: Easysearch grid snapshot is limited to 10000 hits; filter the target or use complete Scroll export", ErrBounds)

// targetBinding is immutable after publication. A document never follows a
// replacement UUID, alias filter/routing change or data-stream rollover, even
// when a later refresh happens to see the original name again.
type targetBinding struct {
	target IndexTarget
	uuids  map[string]string
	scope  json.RawMessage
}

func sameBinding(a, b *targetBinding) bool {
	if a == nil || b == nil || !sameTargetResolution(a.target, b.target) || !sameIndexUUIDs(a.uuids, b.uuids) {
		return false
	}
	return len(a.scope) == 0 && len(b.scope) == 0 || sameNativeJSON(a.scope, b.scope)
}

func readBinding(ctx context.Context, c *Client, objectName, kind string) (*targetBinding, error) {
	targets, err := c.ResolveTargets(ctx, objectName)
	if err != nil {
		return nil, err
	}
	var binding *targetBinding
	for _, target := range targets {
		if target.Name == objectName && target.Kind == kind {
			binding = &targetBinding{target: target}
			break
		}
	}
	if binding == nil {
		return nil, ErrTargetChanged
	}
	// Aliases unrelated to a concrete index do not change its read scope.
	if kind == "index" {
		binding.target.Aliases = nil
	}
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(objectName) + "/_settings/index.uuid?flat_settings=true&expand_wildcards=all"})
	if err != nil {
		return nil, err
	}
	binding.uuids, err = metadataIndexUUIDs(r.Body, binding.target.Members)
	if err != nil {
		return nil, err
	}
	if kind == "alias" {
		r, err = c.Do(ctx, Request{Method: "GET", Path: "/_alias/" + url.PathEscape(objectName)})
		if err != nil {
			return nil, err
		}
		var root map[string]struct {
			Aliases map[string]json.RawMessage `json:"aliases"`
		}
		if decodeJSON(r.Body, &root) != nil || len(root) != len(binding.uuids) {
			return nil, ErrProtocol
		}
		scope := make(map[string]json.RawMessage, len(root))
		for member := range binding.uuids {
			raw := root[member].Aliases[objectName]
			var definition map[string]json.RawMessage
			if decodeJSON(raw, &definition) != nil || definition == nil {
				return nil, ErrProtocol
			}
			scope[member] = bytes.Clone(raw)
		}
		binding.scope, _ = json.Marshal(scope)
	} else if kind == "data_stream" {
		r, err = c.Do(ctx, Request{Method: "GET", Path: "/_data_stream/" + url.PathEscape(objectName)})
		if err != nil {
			return nil, err
		}
		var root struct {
			Streams []struct {
				Name    string `json:"name"`
				Indices []struct {
					Name string `json:"index_name"`
					UUID string `json:"index_uuid"`
				} `json:"indices"`
			} `json:"data_streams"`
		}
		if decodeJSON(r.Body, &root) != nil || len(root.Streams) != 1 || root.Streams[0].Name != objectName {
			return nil, ErrProtocol
		}
		if len(root.Streams[0].Indices) != len(binding.uuids) {
			return nil, ErrTargetChanged
		}
		seen := map[string]bool{}
		for _, member := range root.Streams[0].Indices {
			if seen[member.Name] {
				return nil, ErrProtocol
			}
			seen[member.Name] = true
			if binding.uuids[member.Name] == "" || binding.uuids[member.Name] != member.UUID {
				return nil, ErrTargetChanged
			}
		}
		binding.scope = bytes.Clone(r.Body)
	}
	return binding, nil
}

func (l *dataLease) guard(ctx context.Context, c *Client) (*targetBinding, error) {
	l.mu.Lock()
	changed := l.changed
	l.mu.Unlock()
	if changed {
		return nil, ErrTargetChanged
	}
	// A retained page client can outlive endpoint changes. Prove the cluster
	// again as well as concrete UUIDs; restored UUIDs in another cluster must
	// not grant access through an old qualified document.
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/"})
	if err != nil {
		return nil, err
	}
	var identity Identity
	if decodeJSON(r.Body, &identity) != nil || !validNativeIdentity(identity) || identity.ClusterUUID != l.session.identity.ClusterUUID {
		l.session.revoke()
		return nil, contract.ErrRevoked
	}
	actual, err := readBinding(ctx, c, l.object.Name, l.object.Kind)
	if err != nil {
		var native *HTTPError
		if errors.Is(err, ErrTargetChanged) || errors.As(err, &native) && native.Status == 404 {
			l.mu.Lock()
			l.changed = true
			l.mu.Unlock()
			return nil, errors.Join(ErrTargetChanged, err)
		}
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.changed {
		return nil, ErrTargetChanged
	}
	if l.binding == nil {
		l.binding = actual
	}
	if !sameBinding(l.binding, actual) {
		l.changed = true
		return nil, ErrTargetChanged
	}
	return l.binding, ctx.Err()
}
