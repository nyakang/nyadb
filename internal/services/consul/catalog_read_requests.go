package consul

import (
	"net/url"
	"strconv"
	"strings"
)

// Catalog options retain authored presence and text. Near is a native distance
// ordering only for node and service-instance lists; services and node details
// do not apply it. Datacenter and consistency belong to the original client.
type CatalogReadOptions struct {
	Filter, Near *string
	Index        *uint64
	Wait         *string
}

func catalogReadKind(path string) string {
	switch path {
	case "/v1/catalog/datacenters":
		return "datacenters"
	case "/v1/catalog/nodes":
		return "nodes"
	case "/v1/catalog/services":
		return "services"
	}
	for _, kind := range []string{"service", "node"} {
		if strings.HasPrefix(path, "/v1/catalog/"+kind+"/") {
			return kind
		}
	}
	return ""
}

func WithCatalogReadOptions(request Request, options CatalogReadOptions) (Request, error) {
	kind := catalogReadKind(request.Path)
	if request.Method != "GET" || kind == "" || len(request.Body) != 0 {
		return Request{}, ErrInvalid
	}
	if kind == "datacenters" && (options.Filter != nil || options.Near != nil || options.Index != nil || options.Wait != nil) || options.Near != nil && kind != "nodes" && kind != "service" {
		return Request{}, ErrUnsupported
	}
	r := request.Clone()
	if r.Query == nil {
		r.Query = url.Values{}
	}
	for key, value := range map[string]*string{"filter": options.Filter, "near": options.Near, "wait": options.Wait} {
		if value != nil {
			r.Query.Set(key, *value)
		}
	}
	if options.Index != nil {
		r.Query.Set("index", strconv.FormatUint(*options.Index, 10))
	}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func CatalogDatacentersRequest() Request {
	return Request{Method: "GET", Path: "/v1/catalog/datacenters"}
}

func CatalogNodesRequest() Request {
	return Request{Method: "GET", Path: "/v1/catalog/nodes"}
}

func CatalogServicesRequest() Request {
	return Request{Method: "GET", Path: "/v1/catalog/services"}
}

func catalogNamedRequest(kind, name string) (Request, error) {
	if !validText(name, 4096) || name == "" {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(name) {
		return Request{}, ErrUnsupported
	}
	r := Request{Method: "GET", Path: "/v1/catalog/" + kind + "/" + url.PathEscape(name)}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func CatalogServiceNodesRequest(service string) (Request, error) {
	return catalogNamedRequest("service", service)
}

func CatalogNodeServicesRequest(node string) (Request, error) {
	return catalogNamedRequest("node", node)
}
