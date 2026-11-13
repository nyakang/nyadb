package consul

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func agentProjectionTarget(r contract.KVHTTPRequest) (string, bool) {
	if r.Method != "GET" || r.Body != nil {
		return "", false
	}
	switch r.Path {
	case "/v1/agent/services":
		return "services", true
	case "/v1/agent/checks":
		return "checks", true
	}
	return "", false
}

func projectDocumentAgent(out *contract.KVResult) {
	h := out.HTTP
	kind, ok := agentProjectionTarget(h.Request)
	if !ok || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown {
		return
	}
	list, err := decodeAgentProjection(h.Body, kind)
	if err != nil {
		message := "Agent projection unavailable; original HTTP body and acknowledgement retained: " + err.Error()
		if h.ProjectionError != "" {
			message = h.ProjectionError + "; " + message
		}
		h.ProjectionError, out.ProjectionError = message, message
		return
	}
	list.ProjectionComplete = true
	list.Complete = h.ProjectionError == "" && (h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered)
	h.Agent = &list
}

// Stream the original object order after duplicate/Unicode/depth validation.
// A malformed or oversized map produces no partial inventory.
func decodeAgentProjection(raw []byte, kind string) (contract.KVHTTPAgentList, error) {
	if kind != "services" && kind != "checks" {
		return contract.KVHTTPAgentList{}, ErrUnsupported
	}
	var valid sessionProjectionJSON
	if err := jsondocument.Decode(raw, &valid, responseLimit); err != nil {
		return contract.KVHTTPAgentList{}, errors.Join(ErrProtocol, err)
	}
	list := contract.KVHTTPAgentList{Kind: kind, Rows: []contract.KVHTTPAgentEntry{}}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return contract.KVHTTPAgentList{}, ErrProtocol
	}
	cost := int64(128 + len(kind))
	for d.More() {
		if len(list.Rows) >= entryLimit {
			return contract.KVHTTPAgentList{}, ErrBounds
		}
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !validKey(key, false) {
			return contract.KVHTTPAgentList{}, ErrProtocol
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return contract.KVHTTPAgentList{}, ErrProtocol
		}
		if len(value) > projectionBudget {
			return contract.KVHTTPAgentList{}, ErrBounds
		}
		row, err := decodeAgentEntry(key, value, kind)
		if err != nil {
			return contract.KVHTTPAgentList{}, err
		}
		cost += agentEntryCost(row)
		if cost > projectionBudget {
			return contract.KVHTTPAgentList{}, ErrBounds
		}
		list.Rows = append(list.Rows, row)
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return contract.KVHTTPAgentList{}, ErrProtocol
	}
	if _, err := d.Token(); err != io.EOF {
		return contract.KVHTTPAgentList{}, ErrProtocol
	}
	return list, nil
}

func decodeAgentEntry(key string, raw json.RawMessage, kind string) (contract.KVHTTPAgentEntry, error) {
	fields, present, nulls, err := sessionProjectionFields(raw)
	e := contract.KVHTTPAgentEntry{Key: key, PresentFields: present, NullFields: nulls, Raw: bytes.Clone(raw)}
	if err != nil {
		return e, err
	}
	canonical, alias, service, tags := "ID", "Id", "Service", "Tags"
	if kind == "checks" {
		canonical, alias, service, tags = "CheckID", "CheckId", "ServiceName", "ServiceTags"
	}
	e.ID, err = healthAliasID(fields, canonical, alias)
	if err != nil || e.ID != key {
		return e, ErrProtocol
	}
	if err = healthStrings(fields, nulls, map[string]*string{
		"Name": &e.Name, "Node": &e.Node, "ServiceID": &e.ServiceID, service: &e.Service,
		"Status": &e.Status, "Notes": &e.Notes, "Output": &e.Output, "Kind": &e.Kind,
		"Type": &e.Type, "Address": &e.Address, "Namespace": &e.Namespace, "Partition": &e.Partition,
	}); err != nil {
		return e, err
	}
	if kind == "services" && !validKey(e.Service, false) {
		return e, ErrProtocol
	}
	if err = healthOptionalStringAlias(fields, "ServiceID", "ServiceId", &e.ServiceID); err != nil {
		return e, err
	}
	if err = healthUints(fields, nulls, map[string]*uint64{"Port": &e.Port, "ExposedPort": &e.ExposedPort}); err != nil || e.Port > 65535 || e.ExposedPort > 65535 {
		return e, ErrProtocol
	}
	if v, ok := fields[tags]; ok && !slices.Contains(nulls, tags) {
		e.Tags, err = healthStringList(v)
		if err != nil {
			return e, err
		}
	}
	if v, ok := fields["Meta"]; ok && !slices.Contains(nulls, "Meta") {
		e.Meta, err = healthStringMap(v)
		if err != nil {
			return e, err
		}
	}
	return e, nil
}

func agentEntryCost(e contract.KVHTTPAgentEntry) int64 {
	return int64(384+len(e.Raw)) + healthFieldsCost(e.PresentFields, e.NullFields, e.Tags) +
		healthStringsCost(e.Key, e.ID, e.Name, e.Node, e.ServiceID, e.Service, e.Status, e.Notes, e.Output, e.Kind, e.Type, e.Address, e.Namespace, e.Partition) + healthMapCost(e.Meta)
}

func agentProjectionCost(l contract.KVHTTPAgentList) int64 {
	n := int64(128 + len(l.Kind))
	for _, e := range l.Rows {
		n += agentEntryCost(e)
	}
	return n
}
