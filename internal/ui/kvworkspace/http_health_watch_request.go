package kvworkspace

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func parseHTTPHealthWatchTarget(text string) (contract.KVHTTPHealthWatchTarget, error) {
	var h contract.KVHTTPHealthWatchTarget
	f, err := eventFields(text, "kind", "name", "passing", "filter", "near")
	if err != nil || len(f) < 2 || json.Unmarshal(f["kind"], &h.Kind) != nil || json.Unmarshal(f["name"], &h.Name) != nil || h.Name == "" {
		return h, jsondocument.ErrInvalid
	}
	switch h.Kind {
	case "state":
		if h.Name != "any" && h.Name != "passing" && h.Name != "warning" && h.Name != "critical" {
			return h, jsondocument.ErrInvalid
		}
	case "node", "checks", "service":
	default:
		return h, jsondocument.ErrInvalid
	}
	if raw, ok := f["passing"]; ok {
		if h.Kind != "service" || string(raw) != "false" && string(raw) != "true" {
			return h, jsondocument.ErrInvalid
		}
		h.Passing = string(raw)
	}
	for key, dst := range map[string]*string{"filter": &h.Filter, "near": &h.Near} {
		if raw, ok := f[key]; ok {
			if json.Unmarshal(raw, dst) != nil {
				return h, jsondocument.ErrInvalid
			}
			if key == "filter" {
				h.IncludeFilter = true
			} else {
				h.IncludeNear = true
			}
		}
	}
	for _, value := range []string{h.Name, h.Filter, h.Near} {
		if len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return h, jsondocument.ErrInvalid
		}
	}
	return h, nil
}

// Generation is local and goes through the original provider's pure request
// builder first. It never reserves a Watch lease or starts an observation.
func httpHealthWatchToolRequest(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	h := input.HTTPHealth
	target := map[string]any{}
	switch kind {
	case contract.KVToolHTTPHealthState:
		target["kind"], target["name"] = "state", h.State
	case contract.KVToolHTTPHealthNode:
		target["kind"], target["name"] = "node", h.Node
	case contract.KVToolHTTPHealthChecks:
		target["kind"], target["name"] = "checks", h.Service
	case contract.KVToolHTTPHealthService:
		target["kind"], target["name"] = "service", h.Service
	default:
		return contract.KVRequest{}, jsondocument.ErrInvalid
	}
	if h.Passing != "" {
		if h.Passing != "true" && h.Passing != "false" {
			return contract.KVRequest{}, jsondocument.ErrInvalid
		}
		target["passing"] = h.Passing == "true"
	}
	if h.IncludeFilter {
		target["filter"] = h.Filter
	}
	if h.IncludeNear {
		target["near"] = h.Near
	}
	index, wait := h.Index, h.Wait
	if index == "" {
		index = "0"
	}
	if wait == "" {
		wait = time.Second.String()
	}
	if _, err := strconv.ParseUint(index, 10, 64); err != nil {
		return contract.KVRequest{}, err
	}
	raw, err := json.MarshalIndent(map[string]any{"health": target, "start_index": index, "wait": wait, "max_observations": 32, "max_bytes": "16777216"}, "", "  ")
	if err != nil {
		return contract.KVRequest{}, err
	}
	if _, err := ParseHTTPWatchRequest(string(raw)); err != nil {
		return contract.KVRequest{}, err
	}
	return contract.KVRequest{Method: HTTPWatchMethod, JSON: string(raw)}, nil
}
