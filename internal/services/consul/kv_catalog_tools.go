package consul

import "github.com/nyakang/nyadb/internal/db/contract"

func buildHTTPCatalogTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	c := input.HTTPCatalog
	input.HTTPCatalog = contract.KVHTTPCatalogToolInput{}
	if input != (contract.KVRequestToolInput{}) || !c.IncludeFilter && c.Filter != "" || !c.IncludeNear && c.Near != "" || kind != contract.KVToolHTTPCatalogNode && c.Node != "" || kind != contract.KVToolHTTPCatalogService && c.Service != "" {
		return contract.KVRequest{}, ErrInvalid
	}
	index, err := optionalToolUint(c.Index)
	if err != nil {
		return contract.KVRequest{}, err
	}
	options := CatalogReadOptions{Index: index}
	if c.IncludeFilter {
		options.Filter = &c.Filter
	}
	if c.IncludeNear {
		options.Near = &c.Near
	}
	if c.Wait != "" {
		options.Wait = &c.Wait
	}
	var request Request
	switch kind {
	case contract.KVToolHTTPCatalogDatacenters:
		if c != (contract.KVHTTPCatalogToolInput{}) {
			return contract.KVRequest{}, ErrInvalid
		}
		request = CatalogDatacentersRequest()
	case contract.KVToolHTTPCatalogNodes:
		request = CatalogNodesRequest()
	case contract.KVToolHTTPCatalogServices:
		request = CatalogServicesRequest()
	case contract.KVToolHTTPCatalogService:
		request, err = CatalogServiceNodesRequest(c.Service)
	case contract.KVToolHTTPCatalogNode:
		request, err = CatalogNodeServicesRequest(c.Node)
	default:
		return contract.KVRequest{}, ErrUnsupported
	}
	if err != nil {
		return contract.KVRequest{}, err
	}
	request, err = WithCatalogReadOptions(request, options)
	if err != nil {
		return contract.KVRequest{}, err
	}
	return DocumentRequest(request)
}
