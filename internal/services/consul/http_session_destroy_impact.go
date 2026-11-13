package consul

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func sessionDestroyImpactSession(o contract.KVHTTPSessionDestroyImpact) (contract.KVHTTPSession, error) {
	h := o.Session.HTTP
	if h == nil || h.Kind != "read" || h.Request.Method != "GET" || h.Request.Path != "/v1/session/info/"+o.Request.ID || h.Request.Body != nil || len(h.Request.Query) != 0 || !o.Session.Attempted || !o.Session.Completed || o.Session.WriteOutcomeUnknown || !h.Attempted || !h.Ended || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown || h.CaptureError != "" || h.ProjectionError != "" || o.Session.ProjectionError != "" || h.Status != 200 || h.Sessions == nil || len(h.Sessions.Rows) != 1 || h.Txn != nil || len(h.Entries) != 0 {
		return contract.KVHTTPSession{}, ErrProtocol
	}
	expected, err := DocumentRequest(Request{Method: "GET", Path: "/v1/session/info/" + o.Request.ID})
	if err != nil || o.Session.Request != expected {
		return contract.KVHTTPSession{}, ErrProtocol
	}
	row := h.Sessions.Rows[0]
	if row.ID != o.Request.ID || !slices.Contains(row.PresentFields, "ID") || slices.Contains(row.NullFields, "ID") || !slices.Contains(row.PresentFields, "Behavior") || slices.Contains(row.NullFields, "Behavior") || row.Behavior != "release" && row.Behavior != "delete" || h.Metadata.ACLFiltered != nil && *h.Metadata.ACLFiltered {
		return contract.KVHTTPSession{}, ErrProtocol
	}
	return row, nil
}

// Expectation construction stays in the Consul feature module. A caller gets
// an immutable scoped comparison value only from complete original evidence.
func SessionDestroyExpectation(o contract.KVHTTPSessionDestroyImpact) (contract.KVHTTPSessionDestroyExpectation, error) {
	if !o.Complete || o.Scope.HTTP.Protocol != "consul-http" || o.Scope.Namespace.ProfileID <= 0 || o.Scope.Generation == 0 || !validSessionKeysRequest(contract.KVHTTPSessionKeysRequest{ID: o.Request.ID, MaxRows: o.Request.MaxRows, MaxBytes: o.Request.MaxBytes}) || o.Bytes <= 0 || o.Bytes > o.Request.MaxBytes || !reflect.DeepEqual(o.Scope, o.Keys.Scope) || !o.Keys.Complete || !o.Keys.ProjectionComplete || o.Keys.Request != (contract.KVHTTPSessionKeysRequest{ID: o.Request.ID, MaxRows: o.Request.MaxRows, MaxBytes: o.Request.MaxBytes}) {
		return contract.KVHTTPSessionDestroyExpectation{}, ErrProtocol
	}
	row, err := sessionDestroyImpactSession(o)
	if err != nil {
		return contract.KVHTTPSessionDestroyExpectation{}, err
	}
	keys := o.Keys
	if err := projectSessionKeys(&keys); err != nil || !keys.Complete || !slices.Equal(keys.Matches, o.Keys.Matches) {
		return contract.KVHTTPSessionDestroyExpectation{}, errors.Join(ErrProtocol, err)
	}
	if o.Bytes != keys.Bytes+resultCost(&o.Session)-(1<<20) {
		return contract.KVHTTPSessionDestroyExpectation{}, ErrProtocol
	}
	e := contract.KVHTTPSessionDestroyExpectation{Scope: o.Scope.Clone(), ID: row.ID, Behavior: row.Behavior, HeldKeys: make([]contract.KVHTTPSessionDestroyHeldKey, 0, len(keys.Matches))}
	for _, i := range keys.Matches {
		key := keys.Root.HTTP.Entries[i]
		if key.Key == "" || !slices.Contains(key.PresentFields, "ModifyIndex") {
			return contract.KVHTTPSessionDestroyExpectation{}, ErrProtocol
		}
		e.HeldKeys = append(e.HeldKeys, contract.KVHTTPSessionDestroyHeldKey{Key: key.Key, ModifyIndex: key.ModifyIndex})
	}
	slices.SortFunc(e.HeldKeys, func(a, b contract.KVHTTPSessionDestroyHeldKey) int { return strings.Compare(a.Key, b.Key) })
	for i := 1; i < len(e.HeldKeys); i++ {
		if e.HeldKeys[i-1].Key == e.HeldKeys[i].Key {
			return contract.KVHTTPSessionDestroyExpectation{}, ErrProtocol
		}
	}
	return e, nil
}

func CompareSessionDestroyExpectation(expected contract.KVHTTPSessionDestroyExpectation, actual contract.KVHTTPSessionDestroyImpact) error {
	current, err := SessionDestroyExpectation(actual)
	if err != nil {
		return err
	}
	if expected.Scope.Namespace != current.Scope.Namespace || expected.Scope.Generation != current.Scope.Generation || !bytes.Equal(expected.Scope.Prefix, current.Scope.Prefix) || expected.Scope.HTTP != current.Scope.HTTP || expected.Scope.Header != current.Scope.Header || expected.Scope.Version != current.Scope.Version || expected.ID != current.ID || expected.Behavior != current.Behavior || !slices.Equal(expected.HeldKeys, current.HeldKeys) {
		return ErrConflict
	}
	return nil
}

func (l *documentLease) ObserveHTTPSessionDestroyImpact(ctx context.Context, request contract.KVHTTPSessionDestroyImpactRequest) (out contract.KVHTTPSessionDestroyImpact, err error) {
	return l.observeHTTPSessionDestroyImpact(ctx, request, l.Execute)
}

func (l *documentLease) observeHTTPSessionDestroyImpact(ctx context.Context, request contract.KVHTTPSessionDestroyImpactRequest, execute func(context.Context, contract.KVPreparation) (contract.KVResult, error)) (out contract.KVHTTPSessionDestroyImpact, err error) {
	out.Scope, out.Request = l.Scope().Clone(), request
	keysRequest := contract.KVHTTPSessionKeysRequest{ID: request.ID, MaxRows: request.MaxRows, MaxBytes: request.MaxBytes}
	if !validSessionKeysRequest(keysRequest) {
		return out, ErrInvalid
	}
	native := Request{Method: "GET", Path: "/v1/session/info/" + request.ID}
	document, err := DocumentRequest(native)
	if err != nil {
		return out, err
	}
	prepared, err := l.Prepare(document)
	if err != nil {
		return out, err
	}
	out.Session, err = execute(ctx, prepared)
	if err != nil {
		return out, err
	}
	if _, err = sessionDestroyImpactSession(out); err != nil {
		return out, err
	}
	out.Bytes = resultCost(&out.Session) - 1<<20
	if out.Bytes >= request.MaxBytes {
		return out, ErrBounds
	}
	out.Keys, err = l.observeHTTPSessionKeys(ctx, keysRequest, execute)
	out.Bytes += out.Keys.Bytes
	if err != nil {
		return out, err
	}
	if out.Bytes > request.MaxBytes {
		return out, ErrBounds
	}
	out.Complete = true
	if _, err = SessionDestroyExpectation(out); err != nil {
		out.Complete = false
		return out, err
	}
	if err = l.CheckKVLease(); err != nil {
		out.Complete = false
	}
	return out, err
}

var _ contract.KVHTTPSessionDestroyImpactBrowsing = (*documentLease)(nil)
