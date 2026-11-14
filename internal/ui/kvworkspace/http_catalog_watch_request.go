package kvworkspace

import (
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"strconv"
)

func parseHTTPCatalogWatchTarget(text string) (contract.KVHTTPCatalogWatchTarget, error) {
	var c contract.KVHTTPCatalogWatchTarget
	f, err := eventFields(text, "kind", "name", "filter", "near")
	if err != nil || len(f) < 1 || json.Unmarshal(f["kind"], &c.Kind) != nil {
		return c, jsondocument.ErrInvalid
	}
	if raw, ok := f["name"]; ok {
		if json.Unmarshal(raw, &c.Name) != nil {
			return c, jsondocument.ErrInvalid
		}
	}
	switch c.Kind {
	case "nodes", "services":
		if c.Name != "" {
			return c, jsondocument.ErrInvalid
		}
	case "node", "service":
		if c.Name == "" {
			return c, jsondocument.ErrInvalid
		}
	default:
		return c, jsondocument.ErrInvalid
	}
	for key, dst := range map[string]*string{"filter": &c.Filter, "near": &c.Near} {
		if raw, ok := f[key]; ok {
			if json.Unmarshal(raw, dst) != nil {
				return c, jsondocument.ErrInvalid
			}
			if key == "filter" {
				c.IncludeFilter = true
			} else {
				c.IncludeNear = true
			}
		}
	}
	return c, nil
}
func httpCatalogWatchTool(kind contract.KVRequestToolKind) bool {
	return httpCatalogRequestTool(kind) && kind != contract.KVToolHTTPCatalogDatacenters
}
func httpCatalogWatchToolRequest(kind contract.KVRequestToolKind, in contract.KVRequestToolInput) (contract.KVRequest, error) {
	c := in.HTTPCatalog
	target := map[string]any{}
	switch kind {
	case contract.KVToolHTTPCatalogNodes:
		target["kind"] = "nodes"
	case contract.KVToolHTTPCatalogServices:
		target["kind"] = "services"
	case contract.KVToolHTTPCatalogNode:
		target["kind"], target["name"] = "node", c.Node
	case contract.KVToolHTTPCatalogService:
		target["kind"], target["name"] = "service", c.Service
	default:
		return contract.KVRequest{}, jsondocument.ErrInvalid
	}
	if c.IncludeFilter {
		target["filter"] = c.Filter
	}
	if c.IncludeNear {
		target["near"] = c.Near
	}
	index, wait := c.Index, c.Wait
	if index == "" {
		index = "0"
	}
	if wait == "" {
		wait = "1s"
	}
	if _, err := strconv.ParseUint(index, 10, 64); err != nil {
		return contract.KVRequest{}, err
	}
	raw, err := json.MarshalIndent(map[string]any{"catalog": target, "start_index": index, "wait": wait, "max_observations": 32, "max_bytes": "16777216"}, "", "  ")
	if err != nil {
		return contract.KVRequest{}, err
	}
	if _, err = ParseHTTPWatchRequest(string(raw)); err != nil {
		return contract.KVRequest{}, err
	}
	return contract.KVRequest{Method: HTTPWatchMethod, JSON: string(raw)}, nil
}
