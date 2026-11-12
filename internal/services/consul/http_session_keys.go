package consul

import (
	"bytes"
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func validSessionKeysRequest(request contract.KVHTTPSessionKeysRequest) bool {
	return validID(request.ID) && request.MaxRows > 0 && request.MaxRows <= contract.KVHTTPSessionKeysMaxRows && request.MaxBytes > 0 && request.MaxBytes <= contract.KVHTTPSessionKeysMaxBytes
}

// The target UUID belongs to the captured local projection request. It is not
// added as an unsupported native query flag. The original physical lease adds
// its actual datacenter/consistency; its configured browse prefix never narrows
// this explicitly requested whole-root read.
func sessionKeysDocumentRequest() (contract.KVRequest, error) {
	native, err := KeyListRequest("", false)
	if err != nil {
		return contract.KVRequest{}, err
	}
	return DocumentRequest(native)
}

func projectSessionKeys(o *contract.KVHTTPSessionKeysObservation) error {
	o.Matches, o.ProjectionComplete, o.Complete = nil, false, false
	o.Bytes = 0
	if !validSessionKeysRequest(o.Request) || o.Scope.HTTP.Protocol != "consul-http" || o.Scope.Namespace.ProfileID <= 0 || o.Scope.Generation == 0 {
		return ErrInvalid
	}
	r := &o.Root
	h := r.HTTP
	document, err := sessionKeysDocumentRequest()
	if err != nil || r.Request != document {
		return ErrProtocol
	}
	if h == nil || h.Kind != "kv-read" || h.Request.Method != "GET" || h.Request.Path != "/v1/kv/" || h.Request.Body != nil || len(h.Request.Query) != 1 || len(h.Request.Query["recurse"]) != 1 || h.Request.Query["recurse"][0] != "" {
		return ErrProtocol
	}
	if !r.Attempted || !h.Attempted || !r.Completed || r.WriteOutcomeUnknown || h.WriteOutcomeUnknown || !h.Ended || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.Status != 200 && h.Status != 404 || h.CaptureError != "" || h.ProjectionError != "" || r.ProjectionError != "" || h.Txn != nil || h.Sessions != nil {
		return ErrProtocol
	}
	if h.Status == 404 && len(h.Entries) != 0 || h.Status == 200 && len(h.Entries) == 0 && !bytes.Equal(bytes.TrimSpace(h.Body), []byte("[]")) {
		return ErrProtocol
	}
	// Charge the complete receipt, including unmatched values, query/header
	// fields and duplicated native JSON. The existing fixed lease overhead is
	// independent of this projection budget; all other receipt costs count.
	identity := o.Scope.HTTP
	o.Bytes = resultCost(r) - 1<<20 + int64(256+len(o.Request.ID)+len(o.Scope.Prefix)+len(o.Scope.Version)+len(o.Scope.Namespace.Catalog)+len(o.Scope.Namespace.Schema)+len(identity.Protocol)+len(identity.Endpoint)+len(identity.Datacenter)+len(identity.Consistency)+len(identity.Auth)+len(identity.Configuration)+len(identity.AgentID)+len(identity.AgentName)+len(identity.AgentDatacenter))
	o.Bytes += int64(len(r.Request.Method) + len(h.Kind) + len(h.Protocol) + len(h.SessionID) + len(h.Metadata.QueryBackend) + len(h.Metadata.DefaultACLPolicy))
	if len(h.Entries) > o.Request.MaxRows || o.Bytes > o.Request.MaxBytes {
		return ErrBounds
	}
	matches := make([]int, 0)
	for i, row := range h.Entries {
		if row.Session == o.Request.ID && !row.SessionNull {
			matches = append(matches, i)
		}
	}
	o.Bytes += int64(len(matches) * 8)
	if o.Bytes > o.Request.MaxBytes {
		return ErrBounds
	}
	o.Matches = matches
	o.ProjectionComplete = true
	o.Complete = h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered
	return nil
}

func (l *documentLease) ObserveHTTPSessionKeys(ctx context.Context, request contract.KVHTTPSessionKeysRequest) (out contract.KVHTTPSessionKeysObservation, err error) {
	out.Scope, out.Request = l.Scope().Clone(), request
	if !validSessionKeysRequest(request) {
		return out, ErrInvalid
	}
	document, err := sessionKeysDocumentRequest()
	if err != nil {
		return out, err
	}
	prepared, err := l.Prepare(document)
	if err != nil {
		return out, err
	}
	out.Root, err = l.Execute(ctx, prepared)
	if out.Root.HTTP == nil {
		return out, err
	}
	projectionError := projectSessionKeys(&out)
	return out, errors.Join(err, projectionError)
}

var _ contract.KVHTTPSessionKeysBrowsing = (*documentLease)(nil)
