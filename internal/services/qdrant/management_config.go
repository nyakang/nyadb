package qdrant

import (
	"bytes"
	"encoding/json"
)

func managementEqual(expected, actual json.RawMessage) bool {
	if len(expected) == 0 || len(actual) == 0 {
		return false
	}
	wrap := func(raw json.RawMessage) []byte {
		out := append([]byte(`{"value":`), raw...)
		return append(out, '}')
	}
	// Wrapping preserves nested null distinctly from an empty object; payload
	// equivalence otherwise supplies native numeric normalization and exact IDs.
	return payloadEqual(wrap(expected), wrap(actual))
}

// Native configuration PATCH merges selected parameter fields.
func managementSubset(expected, actual json.RawMessage) bool {
	fields, err := object(expected)
	if err != nil {
		return managementEqual(expected, actual)
	}
	values, err := object(actual)
	if err != nil {
		return false
	}
	for key, value := range fields {
		target, exists := values[key]
		if !exists || !managementSubset(value, target) {
			return false
		}
	}
	return true
}

// Metadata PATCH uses Payload::merge: null removes a top-level key, while an
// object value replaces that key without recursively merging. With no previous
// metadata, Qdrant stores the first payload directly, including its null keys.
func managementMetadataExpected(action string, patch, before json.RawMessage) (json.RawMessage, bool) {
	if action == "collection_create" {
		return patch, true
	}
	if bytes.Equal(bytes.TrimSpace(patch), []byte("null")) {
		return before, true // Option<Payload>::None does not call update_metadata.
	}
	fields, err := object(patch)
	if err != nil {
		return nil, false
	}
	if len(before) == 0 || bytes.Equal(bytes.TrimSpace(before), []byte("null")) {
		return patch, true
	}
	merged, err := object(before)
	if err != nil {
		return nil, false
	}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(merged, key)
		} else {
			merged[key] = value
		}
	}
	raw, err := json.Marshal(merged)
	return raw, err == nil
}

func managementConfigMatches(p ManagementPlan, before, observed json.RawMessage) map[string]bool {
	checks := map[string]bool{}
	requested, requestErr := object(p.request.Body)
	config, configErr := object(observed)
	if requestErr != nil || configErr != nil {
		checks["configuration"] = false
		return checks
	}
	params, _ := object(config["params"])
	paramFields := map[string]bool{"vectors": true, "shard_number": true, "sharding_method": true, "replication_factor": true, "write_consistency_factor": true, "on_disk_payload": true, "payload": true, "id_tracker": true, "sparse_vectors": true}
	for key, raw := range requested {
		if key == "metadata" {
			prior, priorErr := object(before)
			expected, valid := managementMetadataExpected(p.action, raw, prior[key])
			actual := config[key]
			// Optional metadata can be omitted from the native config response.
			if len(expected) == 0 {
				expected = json.RawMessage("null")
			}
			if len(actual) == 0 {
				actual = json.RawMessage("null")
			}
			checks[key] = valid && (p.action == "collection_create" || priorErr == nil) && managementEqual(expected, actual)
			continue
		}
		if key == "params" {
			fields, err := object(raw)
			if err != nil {
				checks[key] = false
				continue
			}
			for name, value := range fields {
				checks[key+"/"+name] = managementSubset(value, params[name])
			}
			continue
		}
		if paramFields[key] {
			checks[key] = managementSubset(raw, params[key])
			continue
		}
		actualKey := key
		if key == "optimizers_config" {
			actualKey = "optimizer_config"
		}
		checks[key] = managementSubset(raw, config[actualKey])
	}
	return checks
}
