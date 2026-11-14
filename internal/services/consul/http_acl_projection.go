package consul

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func projectACLReadResult(scope contract.KVScope, request contract.KVHTTPACLReadRequest, receipt contract.KVResult) contract.KVHTTPACLReadResult {
	out := contract.KVHTTPACLReadResult{Scope: scope.Clone(), Request: request.Clone(), Receipt: receipt}
	h := receipt.HTTP
	if h == nil || !receipt.Completed || receipt.WriteOutcomeUnknown || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown {
		return out
	}
	expected, err := aclNativeReadRequest(request)
	if err != nil || h.Request.Method != expected.Method || h.Request.Path != expected.Path || !reflect.DeepEqual(h.Request.Query, map[string][]string(expected.Query)) || len(h.Request.Body) != 0 {
		out.ProjectionError = "ACL projection unavailable; original receipt retained"
		return out
	}
	p, err := decodeACLProjection(h.Body, request)
	if err != nil {
		// Parser errors may name fields authored by the server. Never include
		// their text in a generic error banner for a secret-bearing response.
		out.ProjectionError = "ACL projection unavailable; original receipt retained"
		return out
	}
	p.ProjectionComplete = true
	p.Complete = !p.Null && h.ProjectionError == "" && (h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered)
	out.Projection = &p
	return out
}

func decodeACLProjection(raw []byte, request contract.KVHTTPACLReadRequest) (contract.KVHTTPACLProjection, error) {
	p := contract.KVHTTPACLProjection{Resource: request.Resource, Lookup: request.Lookup, Rows: []contract.KVHTTPACLRow{}}
	if _, err := aclNativeReadRequest(request); err != nil {
		return p, err
	}
	var checked sessionProjectionJSON
	if jsondocument.Decode(raw, &checked, responseLimit) != nil {
		return p, ErrProtocol
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		p.Null = true
		return p, nil
	}
	cost := int64(128 + len(request.Resource) + len(request.Lookup))
	seen := map[string]bool{}
	add := func(key string, raw json.RawMessage) error {
		row, err := decodeACLRow(raw, request.Resource)
		if err != nil {
			return err
		}
		row.Key = key
		if key != "" && key != row.Identity {
			return ErrProtocol
		}
		if seen[row.Identity] {
			return ErrProtocol
		}
		seen[row.Identity] = true
		if request.Lookup == "id" && row.Identity != request.Identity || request.Lookup == "name" && row.Name != request.Identity {
			return ErrProtocol
		}
		cost += aclRowCost(row)
		if cost > projectionBudget || len(p.Rows) >= entryLimit {
			return ErrBounds
		}
		p.ContainsRedaction = p.ContainsRedaction || row.SecretState == "redacted" || row.ConfigState == "redacted"
		p.ContainsSensitiveValues = p.ContainsSensitiveValues || row.SecretState == "value" || row.ConfigState == "value" || row.ConfigState == "redacted"
		p.Rows = append(p.Rows, row)
		return nil
	}
	var err error
	if request.Lookup != "list" {
		err = add("", raw)
	} else if request.Resource == string(ACLReadTemplatedPolicy) {
		err = catalogProjectionObject(raw, add)
	} else {
		err = sessionProjectionArray(raw, entryLimit, func(value json.RawMessage) error { return add("", value) })
	}
	if err != nil {
		return contract.KVHTTPACLProjection{}, err
	}
	return p, nil
}

func decodeACLRow(raw []byte, resource string) (contract.KVHTTPACLRow, error) {
	row := contract.KVHTTPACLRow{Raw: bytes.Clone(raw), SecretState: "absent", ConfigState: "absent"}
	fields, present, nulls, err := sessionProjectionFields(raw)
	if err != nil {
		return row, err
	}
	row.PresentFields, row.NullFields = present, nulls
	identityField := "ID"
	switch ACLReadResource(resource) {
	case ACLReadToken:
		identityField = "AccessorID"
	case ACLReadAuthMethod:
		identityField = "Name"
	case ACLReadTemplatedPolicy:
		identityField = "TemplateName"
	}
	if json.Unmarshal(fields[identityField], &row.Identity) != nil || row.Identity == "" {
		return row, ErrProtocol
	}
	if identityField == "ID" || identityField == "AccessorID" {
		if !validID(row.Identity) {
			return row, ErrProtocol
		}
	}
	if err := healthStrings(fields, nulls, map[string]*string{"Name": &row.Name, "Description": &row.Description}); err != nil {
		return row, err
	}
	if identityField == "TemplateName" {
		row.Name = row.Identity
	}
	for field, target := range map[string]**uint64{"CreateIndex": &row.CreateIndex, "ModifyIndex": &row.ModifyIndex} {
		if value, exists := fields[field]; exists && !slices.Contains(nulls, field) {
			var number uint64
			if json.Unmarshal(value, &number) != nil {
				return row, ErrProtocol
			}
			*target = &number
		}
	}
	if value, exists := fields["SecretID"]; exists {
		if slices.Contains(nulls, "SecretID") {
			row.SecretState = "null"
		} else {
			var secret string
			if json.Unmarshal(value, &secret) != nil {
				return row, ErrProtocol
			}
			row.SecretState = "value"
			if secret == "" {
				row.SecretState = "empty"
			}
			if secret == "<hidden>" {
				row.SecretState = "redacted"
			}
		}
	}
	if value, exists := fields["Config"]; exists && resource == string(ACLReadAuthMethod) {
		if slices.Contains(nulls, "Config") {
			row.ConfigState = "null"
		} else {
			var config map[string]json.RawMessage
			if json.Unmarshal(value, &config) != nil || config == nil {
				return row, ErrProtocol
			}
			row.ConfigState = "value"
			if len(config) == 0 {
				row.ConfigState = "empty"
			}
			// The complete raw Config stays private even if all currently
			// recognized secret fields happen to be redacted by this server.
			redacted, err := aclConfigRedacted(value)
			if err != nil {
				return row, err
			}
			if redacted {
				row.ConfigState = "redacted"
			}
		}
	}
	return row, nil
}

// Walk values only: a field named "<hidden>" is not a redacted value. The
// complete document has already passed duplicate, Unicode and depth validation.
func aclConfigRedacted(raw []byte) (bool, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	redacted := false
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return ErrBounds
		}
		token, err := d.Token()
		if err != nil {
			return ErrProtocol
		}
		switch token := token.(type) {
		case string:
			redacted = redacted || token == "<hidden>"
		case json.Delim:
			if token != '{' && token != '[' {
				return ErrProtocol
			}
			for d.More() {
				if token == '{' {
					key, err := d.Token()
					if _, ok := key.(string); err != nil || !ok {
						return ErrProtocol
					}
				}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || token == '{' && end != json.Delim('}') || token == '[' && end != json.Delim(']') {
				return ErrProtocol
			}
		}
		return nil
	}
	if err := value(0); err != nil {
		return false, err
	}
	if _, err := d.Token(); err != io.EOF {
		return false, ErrProtocol
	}
	return redacted, nil
}

func aclRowCost(row contract.KVHTTPACLRow) int64 {
	cost := int64(256 + len(row.Key) + len(row.Identity) + len(row.Name) + len(row.Description) + len(row.Raw) + len(row.SecretState) + len(row.ConfigState))
	for _, fields := range [][]string{row.PresentFields, row.NullFields} {
		for _, value := range fields {
			cost += int64(24 + len(value))
		}
	}
	return cost
}
