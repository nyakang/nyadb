package solr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// NativeDocumentRef names one concrete member and its native routing inputs.
// It does not assert a schema, immutable shard identity or original source.
// The document/workbench layer must bind and verify those before offering edits.
type NativeDocumentRef struct {
	Member, UniqueKey, Router, Shard, RouteField string
	Key, RouteValue                              contract.Cell
}

// NativeDocument is the exact returned RTG projection, not submitted source.
// copy fields and visibility can differ between RTG and a search projection.
type NativeDocument struct {
	Ref        NativeDocumentRef
	Version    json.Number
	Projection json.RawMessage
}

type NativeDocumentMutation struct {
	Kind    string // create | update | delete; exactly one document per request
	Ref     NativeDocumentRef
	Version json.Number // -1 for create; exact original positive version for edits
	Fields  map[string]json.RawMessage
}

type NativeDocumentReceipt struct {
	Ref             NativeDocumentRef
	Kind            string
	Response        Response
	State           string      // acknowledged | uncertain; never a durable/batch commit claim
	Version         json.Number // mutation clock when known; never the echoed precondition
	ReportedVersion json.Number // native token, including distributed zero/echoed precondition
}

var (
	ErrDocumentMutationUncertain = errors.New("Solr document mutation outcome is uncertain; inspect the original target without replay")
	ErrDocumentNotFound          = errors.New("Solr native document not found")
)

func emptyNativeCell(c contract.Cell) bool {
	return c.Kind == "" && c.Text == "" && c.Bytes == nil && !c.Truncated
}

func nativeNumber(raw []byte) (json.Number, error) {
	var value any
	if decodeJSON(raw, &value) != nil {
		return "", ErrProtocol
	}
	number, ok := value.(json.Number)
	if !ok {
		return "", ErrProtocol
	}
	return number, nil
}

func nativeScalar(cell contract.Cell) (any, error) {
	if cell.Truncated || cell.Bytes != nil {
		return nil, ErrInvalid
	}
	switch cell.Kind {
	case contract.Text:
		if len(cell.Text) > 512 || !utf8.ValidString(cell.Text) || strings.ContainsRune(cell.Text, 0) {
			return nil, ErrInvalid
		}
		return cell.Text, nil
	case contract.Integer:
		if _, err := strconv.ParseInt(cell.Text, 10, 64); err != nil {
			return nil, ErrInvalid
		}
		return json.Number(cell.Text), nil
	default:
		return nil, ErrInvalid
	}
}

func (c *Client) validateDocumentRef(ref NativeDocumentRef) error {
	if !validTargetName(ref.Member) || !nativeFieldName.MatchString(ref.UniqueKey) || ref.UniqueKey == "_version_" {
		return ErrInvalid
	}
	if _, err := nativeScalar(ref.Key); err != nil {
		return err
	}
	if c.identity.Mode == "std" {
		if ref.Router != "" || ref.Shard != "" || ref.RouteField != "" || !emptyNativeCell(ref.RouteValue) {
			return ErrInvalid
		}
		return nil
	}
	if ref.Router != "implicit" && ref.Router != "compositeId" {
		return contract.ErrUnsupported
	}
	if ref.Shard != "" && !validTargetName(ref.Shard) || ref.Router == "implicit" && ref.Shard == "" {
		return ErrInvalid
	}
	if ref.RouteField == "" {
		if !emptyNativeCell(ref.RouteValue) {
			return ErrInvalid
		}
	} else if !nativeFieldName.MatchString(ref.RouteField) || ref.RouteField == ref.UniqueKey || ref.RouteField == "_version_" {
		return ErrInvalid
	} else if _, err := nativeScalar(ref.RouteValue); err != nil {
		return err
	} else if ref.Router == "implicit" && ref.RouteValue.Text != ref.Shard {
		return ErrInvalid
	}
	return nil
}

func documentParams(ref NativeDocumentRef) url.Values {
	p := url.Values{"wt": {"json"}, "omitHeader": {"false"}, "echoParams": {"none"}}
	if ref.Router == "implicit" {
		p.Set("_route_", ref.Shard)
	} else if ref.RouteField != "" {
		p.Set("_route_", ref.RouteValue.Text)
	}
	return p
}

// ReadNativeDocument uses native RTG routing. A compositeId Shard is an
// observation, not an override: add/update routing uses the id or route field.
// Exact original version comparison is required at the owning document layer.
func (c *Client) ReadNativeDocument(ctx context.Context, ref NativeDocumentRef) (*NativeDocument, error) {
	if err := c.validateDocumentRef(ref); err != nil {
		return nil, err
	}
	p := documentParams(ref)
	p.Set("id", ref.Key.Text)
	p.Set("fl", "*,"+ref.UniqueKey+",_version_")
	if ref.Router == "" {
		p.Set("distrib", "false")
		p.Set("shards", "")
	}
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(ref.Member) + "/get?" + p.Encode()})
	if err != nil {
		return nil, err
	}
	var receipt struct {
		Header *struct{ Status *int } `json:"responseHeader"`
		Doc    json.RawMessage        `json:"doc"`
	}
	if decodeJSON(r.Body, &receipt) != nil || receipt.Header == nil || receipt.Header.Status == nil || *receipt.Header.Status != 0 || len(receipt.Doc) == 0 {
		return nil, ErrProtocol
	}
	if bytes.Equal(receipt.Doc, []byte("null")) {
		return nil, ErrDocumentNotFound
	}
	var fields map[string]json.RawMessage
	if decodeJSON(receipt.Doc, &fields) != nil || fields == nil {
		return nil, ErrProtocol
	}
	key, err := keyCell(fields[ref.UniqueKey])
	if err != nil || !sameKey(key, ref.Key) {
		return nil, ErrProtocol
	}
	version, err := nativeNumber(fields["_version_"])
	if err != nil {
		return nil, ErrProtocol
	}
	if n, e := version.Int64(); e != nil || n <= 1 {
		return nil, ErrProtocol
	}
	return &NativeDocument{Ref: ref, Version: version, Projection: bytes.Clone(receipt.Doc)}, ctx.Err()
}

func flatNativeValue(raw []byte) bool {
	var value any
	if decodeJSON(raw, &value) != nil {
		return false
	}
	scalar := func(v any) bool {
		switch v.(type) {
		case string, bool, json.Number:
			return true
		}
		return false
	}
	if value == nil || scalar(value) {
		return true
	}
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, v := range values {
		if !scalar(v) {
			return false
		}
	}
	return true
}

func (c *Client) nativeMutationRequest(m NativeDocumentMutation) (Request, error) {
	if err := c.validateDocumentRef(m.Ref); err != nil {
		return Request{}, err
	}
	v, err := m.Version.Int64()
	if err != nil || m.Kind == "create" && v != -1 || m.Kind != "create" && v <= 1 {
		return Request{}, ErrInvalid
	}
	key, _ := nativeScalar(m.Ref.Key)
	var body any
	switch m.Kind {
	case "create", "update":
		if m.Kind == "update" && len(m.Fields) == 0 || len(m.Fields) > 8192 {
			return Request{}, ErrBounds
		}
		doc := map[string]any{m.Ref.UniqueKey: key, "_version_": m.Version}
		size := 0
		for name, value := range m.Fields {
			size += len(name) + len(value)
			if size > 1<<20 {
				return Request{}, ErrBounds
			}
			reserved := name == "_version_" || name == "_root_" || name == "_nest_path_" || name == "_childDocuments_" || name == "_route_"
			if !nativeFieldName.MatchString(name) || name == m.Ref.UniqueKey || name == m.Ref.RouteField || reserved || !flatNativeValue(value) {
				return Request{}, ErrInvalid
			}
			if m.Kind == "update" {
				doc[name] = map[string]json.RawMessage{"set": bytes.Clone(value)}
			} else {
				doc[name] = json.RawMessage(bytes.Clone(value))
			}
		}
		if m.Ref.RouteField != "" {
			doc[m.Ref.RouteField], _ = nativeScalar(m.Ref.RouteValue)
		}
		body = []any{doc}
	case "delete":
		if len(m.Fields) != 0 {
			return Request{}, ErrInvalid
		}
		body = map[string]any{"delete": map[string]any{"id": key, "_version_": m.Version}}
	default:
		return Request{}, contract.ErrUnsupported
	}
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > 1<<20 {
		return Request{}, ErrBounds
	}
	p := documentParams(m.Ref)
	p.Set("versions", "true")
	// Deliberately no commit/commitWithin or retry. A hard commit can affect
	// other documents; RTG verification and durable commit are distinct.
	return Request{Method: "POST", Path: "/" + url.PathEscape(m.Ref.Member) + "/update?" + p.Encode(), Body: raw}, nil
}

// MutateNativeDocument sends exactly one native create/atomic-set/delete.
// Schema recovery, copy/nested fields, handler/processor binding, original
// target validation and readback belong to the owning feature, not this codec.
func (c *Client) MutateNativeDocument(ctx context.Context, m NativeDocumentMutation) (*NativeDocumentReceipt, error) {
	return c.mutateNativeDocument(ctx, m, "")
}

func (c *Client) mutateNativeDocument(ctx context.Context, m NativeDocumentMutation, chain string) (*NativeDocumentReceipt, error) {
	request, err := c.nativeMutationRequest(m)
	if err != nil {
		return nil, err
	}
	if chain != "" {
		if !nativeFieldName.MatchString(chain) {
			return nil, ErrInvalid
		}
		request.Path += "&update.chain=" + url.QueryEscape(chain)
	}
	r, err := c.Do(ctx, request)
	if err != nil {
		var interrupted *Interrupted
		if errors.As(err, &interrupted) && interrupted.WriteOutcomeUnknown {
			return &NativeDocumentReceipt{Ref: m.Ref, Kind: m.Kind, Response: r, State: "uncertain"}, errors.Join(ErrDocumentMutationUncertain, err)
		}
		return nil, err
	}
	uncertain := func() (*NativeDocumentReceipt, error) {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return &NativeDocumentReceipt{Ref: m.Ref, Kind: m.Kind, Response: r, State: "uncertain"}, errors.Join(ErrDocumentMutationUncertain, ErrProtocol)
	}
	var receipt struct {
		Header *struct {
			Status  *int
			Partial json.RawMessage `json:"partialResults"`
		} `json:"responseHeader"`
		Adds, Deletes []json.RawMessage
	}
	if decodeJSON(r.Body, &receipt) != nil || receipt.Header == nil || receipt.Header.Status == nil || *receipt.Header.Status != 0 || len(receipt.Header.Partial) > 0 && !bytes.Equal(receipt.Header.Partial, []byte("false")) {
		return uncertain()
	}
	tokens, other := receipt.Adds, receipt.Deletes
	if m.Kind == "delete" {
		tokens, other = other, tokens
	}
	if len(tokens) != 2 || len(other) > 0 {
		return uncertain()
	}
	var key string
	version, versionErr := nativeNumber(tokens[1])
	if decodeJSON(tokens[0], &key) != nil || key != m.Ref.Key.Text || versionErr != nil {
		return uncertain()
	}
	n, err := version.Int64()
	if err != nil || n != 0 && n >= -1 && n <= 1 || n == 0 && c.identity.Mode != "solrcloud" || m.Kind != "delete" && n < 0 {
		return uncertain()
	}
	reported := version
	if m.Kind == "delete" && n > 0 {
		// A forwarding Cloud entry can retain cmd.getVersion() from the
		// original delete precondition, rather than the remote tombstone.
		if c.identity.Mode != "solrcloud" || version != m.Version {
			return uncertain()
		}
		version = ""
	} else if n == 0 {
		version = ""
	}
	return &NativeDocumentReceipt{Ref: m.Ref, Kind: m.Kind, Response: r, State: "acknowledged", Version: version, ReportedVersion: reported}, ctx.Err()
}
