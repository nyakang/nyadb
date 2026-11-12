package consul

import (
	"net/url"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Read builders retain authored nodes without opening a session or adding
// credentials. The original document lease supplies connection scope at run.
func buildHTTPSessionListTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	node := input.HTTPSession.Node
	input.HTTPSession.Node = ""
	if input != (contract.KVRequestToolInput{}) {
		return contract.KVRequest{}, ErrInvalid
	}
	path := "/v1/session/list"
	switch kind {
	case contract.KVToolHTTPSessionList:
		if node != "" {
			return contract.KVRequest{}, ErrInvalid
		}
	case contract.KVToolHTTPNodeSessions:
		if len(node) > 4096 {
			return contract.KVRequest{}, ErrBounds
		}
		if node == "" || !validText(node, 4096) {
			return contract.KVRequest{}, ErrInvalid
		}
		if !canonicalHTTPKey(node) {
			return contract.KVRequest{}, ErrUnsupported
		}
		path = "/v1/session/node/" + url.PathEscape(node)
	default:
		return contract.KVRequest{}, ErrUnsupported
	}
	return DocumentRequest(Request{Method: "GET", Path: path})
}
