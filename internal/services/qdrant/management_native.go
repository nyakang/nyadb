package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
)

var ErrManagementUsed = errors.New("original Qdrant management request already attempted; retain evidence without replay")

// ManagementPlan captures one native collection or alias request. Copies share
// its one-use attempt state; Preview returns independent bytes. It supplies no
// remote collection incarnation, CAS, namespace claim or permission guarantee.
type ManagementPlan struct {
	action, collection string
	request            Request
	used               *atomic.Bool
}

func (p ManagementPlan) Action() string     { return p.action }
func (p ManagementPlan) Collection() string { return p.collection }
func (p ManagementPlan) Preview() Request {
	r := p.request
	r.Body = bytes.Clone(r.Body)
	return r
}

func managementTimeout(seconds int) (string, error) {
	if seconds < 1 || seconds > 300 {
		return "", ErrInvalid
	}
	return "?timeout=" + strconv.Itoa(seconds), nil
}

// PrepareCollectionManagement preserves explicit native JSON and lets Qdrant
// validate the nested schema. Unknown top-level operations/configuration fields
// are rejected locally; a successful boolean still requires metadata readback.
func PrepareCollectionManagement(collection, action string, config json.RawMessage, timeoutSeconds int) (ManagementPlan, error) {
	path, err := collectionPath(collection)
	if err != nil {
		return ManagementPlan{}, err
	}
	timeout, err := managementTimeout(timeoutSeconds)
	if err != nil {
		return ManagementPlan{}, err
	}
	r := Request{Path: path + timeout, ContentType: "application/json"}
	var fields map[string]json.RawMessage
	var allowed map[string]bool
	switch action {
	case "collection_create":
		r.Method = "PUT"
		allowed = map[string]bool{"vectors": true, "shard_number": true, "sharding_method": true, "replication_factor": true, "write_consistency_factor": true, "on_disk_payload": true, "payload": true, "id_tracker": true, "hnsw_config": true, "wal_config": true, "optimizers_config": true, "quantization_config": true, "sparse_vectors": true, "strict_mode_config": true, "metadata": true}
	case "collection_update":
		r.Method = "PATCH"
		allowed = map[string]bool{"vectors": true, "optimizers_config": true, "params": true, "hnsw_config": true, "quantization_config": true, "sparse_vectors": true, "strict_mode_config": true, "metadata": true}
	case "collection_delete":
		if len(config) != 0 {
			return ManagementPlan{}, ErrInvalid
		}
		r.Method = "DELETE"
	default:
		return ManagementPlan{}, ErrInvalid
	}
	if allowed != nil {
		if len(config) > requestLimit {
			return ManagementPlan{}, ErrBounds
		}
		fields, err = object(config)
		if err != nil || action == "collection_update" && len(fields) == 0 {
			return ManagementPlan{}, ErrInvalid
		}
		for key := range fields {
			if !allowed[key] {
				return ManagementPlan{}, ErrInvalid
			}
		}
		r.Body = bytes.Clone(config)
	}
	if err := validateRequest(r); err != nil {
		return ManagementPlan{}, err
	}
	return ManagementPlan{action: action, collection: collection, request: r, used: &atomic.Bool{}}, nil
}

// AliasChange expresses one native action. Retargeting is an explicit delete
// and create in the same ordered request; rename does not rename a collection.
type AliasChange struct {
	Action, Alias, Collection, Destination string
}

func PrepareAliasManagement(changes []AliasChange, timeoutSeconds int) (ManagementPlan, error) {
	if len(changes) == 0 || len(changes) > 1000 {
		return ManagementPlan{}, ErrBounds
	}
	timeout, err := managementTimeout(timeoutSeconds)
	if err != nil {
		return ManagementPlan{}, err
	}
	actions := make([]map[string]any, len(changes))
	for i, change := range changes {
		if _, err := collectionPath(change.Alias); err != nil {
			return ManagementPlan{}, err
		}
		switch change.Action {
		case "create_alias":
			if _, err := collectionPath(change.Collection); err != nil || change.Destination != "" {
				return ManagementPlan{}, ErrInvalid
			}
			actions[i] = map[string]any{"create_alias": map[string]string{"collection_name": change.Collection, "alias_name": change.Alias}}
		case "delete_alias":
			if change.Collection != "" || change.Destination != "" {
				return ManagementPlan{}, ErrInvalid
			}
			actions[i] = map[string]any{"delete_alias": map[string]string{"alias_name": change.Alias}}
		case "rename_alias":
			if _, err := collectionPath(change.Destination); err != nil || change.Destination == change.Alias || change.Collection != "" {
				return ManagementPlan{}, ErrInvalid
			}
			actions[i] = map[string]any{"rename_alias": map[string]string{"old_alias_name": change.Alias, "new_alias_name": change.Destination}}
		default:
			return ManagementPlan{}, ErrInvalid
		}
	}
	body, err := json.Marshal(map[string]any{"actions": actions})
	if err != nil || len(body) > requestLimit {
		return ManagementPlan{}, ErrBounds
	}
	r := Request{Method: "POST", Path: "/collections/aliases" + timeout, ContentType: "application/json", Body: body}
	return ManagementPlan{action: "aliases", request: r, used: &atomic.Bool{}}, nil
}

// ManagementReceipt preserves a settled native boolean when present. Both
// true and false are responses, neither certifies the desired metadata state.
// There is no task/operation ID on these native endpoints.
type ManagementReceipt struct {
	Action, Collection string
	HasResult, Result  bool
	Response           Response
}

func (c *Client) SubmitManagement(ctx context.Context, p ManagementPlan) (ManagementReceipt, error) {
	receipt := ManagementReceipt{Action: p.action, Collection: p.collection}
	if p.used == nil || p.action == "" {
		return receipt, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if !p.used.CompareAndSwap(false, true) {
		return receipt, ErrManagementUsed
	}
	r, err := c.Do(ctx, p.Preview())
	receipt.Response = r
	if err != nil {
		return receipt, err
	}
	result, err := NativeResult(r.Body)
	if r.Status != 200 || r.Incomplete || err != nil || !bytes.Equal(result, []byte("true")) && !bytes.Equal(result, []byte("false")) {
		c.retire()
		return receipt, &Interrupted{WriteOutcomeUnknown: true, Cause: ErrProtocol}
	}
	receipt.HasResult, receipt.Result = true, bytes.Equal(result, []byte("true"))
	return receipt, nil
}
