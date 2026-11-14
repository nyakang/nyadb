package consul

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

type managementOperation struct {
	contract.KVHTTPManagementOperation
	ack, routing string
}

// routing is endpoint-specific. Agent, keyring and license operations never
// inherit a KV prefix, datacenter or read consistency the endpoint ignores.
var managementOperations = []managementOperation{
	managementOp("agent-self", "Agent identity", "GET", "/v1/agent/self", "", "json", "local", "", ""),
	managementOp("probe-catalog", "Catalog availability", "GET", "/v1/catalog/datacenters", "", "json", "local", "", ""),
	managementOp("probe-health", "Health availability", "GET", "/v1/health/state/any", "", "json", "scope-read", "", ""),
	managementOp("probe-sessions", "Sessions availability", "GET", "/v1/session/list", "", "json", "scope-read", "", ""),
	managementOp("probe-acl", "ACL availability", "GET", "/v1/acl/token/self", "", "json", "scope-read", "", ""),
	managementOp("probe-auth-methods", "Auth methods availability", "GET", "/v1/acl/auth-methods", "", "json", "agent", "", ""),
	managementOp("probe-binding-rules", "Binding rules availability", "GET", "/v1/acl/binding-rules", "", "json", "agent", "", ""),
	managementOp("probe-acl-templates", "ACL templates availability", "GET", "/v1/acl/templated-policies", "", "json", "local", "", ""),
	managementOp("agent-members", "Agent members", "GET", "/v1/agent/members", "", "json", "local", "", "wan segment"),
	managementOp("agent-metrics", "Agent metrics", "GET", "/v1/agent/metrics", "", "json", "local", "", "format"),
	managementOp("agent-service", "Agent service details", "GET", "/v1/agent/service/:id", "", "json", "agent", "", ""),
	managementOp("leader", "Leader", "GET", "/v1/status/leader", "", "json", "dc", "", ""),
	managementOp("peers", "Raft peers", "GET", "/v1/status/peers", "", "json", "dc", "", ""),
	managementOp("autopilot-config", "Autopilot configuration", "GET", "/v1/operator/autopilot/configuration", "", "json", "dc", "", ""),
	managementOp("autopilot-health", "Autopilot health", "GET", "/v1/operator/autopilot/health", "", "json", "dc", "", ""),
	managementOp("autopilot-state", "Autopilot state", "GET", "/v1/operator/autopilot/state", "", "json", "dc", "", ""),
	managementOp("autopilot-update", "Update Autopilot", "PUT", "/v1/operator/autopilot/configuration", "json", "bool", "dc", "autopilot", "cas"),
	managementOp("raft-config", "Raft configuration", "GET", "/v1/operator/raft/configuration", "", "json", "dc", "", "stale"),
	managementOp("raft-transfer", "Transfer Raft leadership", "POST", "/v1/operator/raft/transfer-leader", "", "success", "dc", "raft", "id"),
	managementOp("raft-remove", "Remove Raft peer", "DELETE", "/v1/operator/raft/peer", "", "empty", "dc", "raft", "id address"),
	managementOp("keyring-list", "Gossip keyring", "GET", "/v1/operator/keyring", "", "json", "local", "", "local-only relay-factor"),
	managementOp("keyring-install", "Install gossip key", "POST", "/v1/operator/keyring", "json", "empty", "local", "keyring", "relay-factor"),
	managementOp("keyring-use", "Use gossip key", "PUT", "/v1/operator/keyring", "json", "empty", "local", "keyring", "relay-factor"),
	managementOp("keyring-remove", "Remove gossip key", "DELETE", "/v1/operator/keyring", "json", "empty", "local", "keyring", "relay-factor"),
	managementOp("usage", "Operator usage", "GET", "/v1/operator/usage", "", "json", "scope-read", "", "global"),
	managementOp("license-get", "Enterprise license", "GET", "/v1/operator/license", "", "json", "local", "", ""),
	managementOp("license-put", "Update Enterprise license", "PUT", "/v1/operator/license", "bytes", "empty", "local", "license", ""),
	managementOp("audit-hash", "Enterprise audit hash", "POST", "/v1/operator/audit-hash", "json", "json", "local", "", ""),
	managementOp("query-list", "Prepared Query list", "GET", "/v1/query", "", "json", "dc-read", "", "index wait"),
	managementOp("query-get", "Prepared Query details", "GET", "/v1/query/:id", "", "json", "dc-read", "", "index wait"),
	managementOp("query-create", "Create Prepared Query", "POST", "/v1/query", "json", "id", "dc", "", ""),
	managementOp("query-update", "Update Prepared Query", "PUT", "/v1/query/:id", "json", "empty", "dc", "", ""),
	managementOp("query-delete", "Delete Prepared Query", "DELETE", "/v1/query/:id", "", "empty", "dc", "", ""),
	managementOp("query-execute", "Execute Prepared Query", "GET", "/v1/query/:id/execute", "", "json", "dc-read", "", "limit connect near"),
	managementOp("query-explain", "Explain Prepared Query", "GET", "/v1/query/:id/explain", "", "json", "dc-read", "", ""),
	managementOp("event-list", "Event list", "GET", "/v1/event/list", "", "json", "dc", "", "name index wait"),
	managementOp("event-fire", "Fire event", "PUT", "/v1/event/fire/:id", "bytes", "event", "dc", "", "node service tag"),
	managementOp("coordinate-nodes", "Network coordinates", "GET", "/v1/coordinate/nodes", "", "json", "dc-read", "", "segment index wait"),
	managementOp("config-list", "Config Entry list", "GET", "/v1/config/:kind", "", "json", "scope-read", "", "filter index wait"),
	managementOp("config-get", "Config Entry details", "GET", "/v1/config/:kind/:id", "", "json", "scope-read", "", "index wait"),
	managementOp("config-apply", "Apply Config Entry", "PUT", "/v1/config", "json", "bool", "scope", "", "cas"),
	managementOp("config-delete", "Delete Config Entry", "DELETE", "/v1/config/:kind/:id", "", "bool", "scope", "", "cas"),
	managementOp("intentions-list", "Intentions", "GET", "/v1/connect/intentions", "", "json", "scope-read", "", "filter index wait"),
	managementOp("intention-get", "Intention details", "GET", "/v1/connect/intentions/:id", "", "json", "scope-read", "", "index wait"),
	managementOp("intention-create", "Create intention", "POST", "/v1/connect/intentions", "json", "id", "scope", "", ""),
	managementOp("intention-update", "Update intention", "PUT", "/v1/connect/intentions/:id", "json", "id", "scope", "", ""),
	managementOp("intention-delete", "Delete intention", "DELETE", "/v1/connect/intentions/:id", "", "bool", "scope", "", ""),
	managementOp("intention-exact-get", "Read exact intention", "GET", "/v1/connect/intentions/exact", "", "json", "scope-read", "", "source destination index wait"),
	managementOp("intention-exact-put", "Apply exact intention", "PUT", "/v1/connect/intentions/exact", "json", "bool", "scope", "", "source destination"),
	managementOp("intention-exact-delete", "Delete exact intention", "DELETE", "/v1/connect/intentions/exact", "", "bool", "scope", "", "source destination"),
	managementOp("intention-match", "Match intentions", "GET", "/v1/connect/intentions/match", "", "json", "scope-read", "", "by name"),
	managementOp("intention-check", "Check intention", "GET", "/v1/connect/intentions/check", "", "json", "scope-read", "", "source destination source-type"),
	managementOp("discovery-chain", "Discovery Chain", "GET", "/v1/discovery-chain/:id", "", "json", "scope-read", "", "compile-dc index wait"),
	managementOp("peering-list", "Peering list", "GET", "/v1/peerings", "", "json", "partition-read", "", "index wait"),
	managementOp("peering-get", "Peering details", "GET", "/v1/peering/:id", "", "json", "partition-read", "", "index wait"),
	managementOp("peering-token", "Generate Peering token", "POST", "/v1/peering/token", "json", "peering-token", "partition", "", ""),
	managementOp("peering-establish", "Establish Peering", "POST", "/v1/peering/establish", "json", "object", "partition", "", ""),
	managementOp("peering-delete", "Delete Peering", "DELETE", "/v1/peering/:id", "", "empty", "partition", "", ""),
	managementOp("exported-services", "Exported Services", "GET", "/v1/exported-services", "", "json", "partition-read", "", ""),
	managementOp("namespace-list", "Enterprise namespaces", "GET", "/v1/namespaces", "", "json", "partition-read", "", "index wait"),
	managementOp("namespace-get", "Enterprise namespace details", "GET", "/v1/namespace/:id", "", "json", "partition-read", "", ""),
	managementOp("namespace-create", "Create Enterprise namespace", "PUT", "/v1/namespace", "json", "name", "partition", "", ""),
	managementOp("namespace-update", "Update Enterprise namespace", "PUT", "/v1/namespace/:id", "json", "name", "partition", "", ""),
	managementOp("namespace-delete", "Delete Enterprise namespace", "DELETE", "/v1/namespace/:id", "", "empty", "partition", "", ""),
	managementOp("partition-list", "Enterprise partitions", "GET", "/v1/partitions", "", "json", "dc", "", ""),
	managementOp("partition-get", "Enterprise partition details", "GET", "/v1/partition/:id", "", "json", "dc", "", ""),
	managementOp("partition-create", "Create Enterprise partition", "PUT", "/v1/partition", "json", "name", "dc", "", ""),
	managementOp("partition-update", "Update Enterprise partition", "PUT", "/v1/partition/:id", "json", "name", "dc", "", ""),
	managementOp("partition-delete", "Delete Enterprise partition", "DELETE", "/v1/partition/:id", "", "empty", "dc", "", ""),
}

func managementOp(name, title, method, path, body, ack, routing, guard, options string) managementOperation {
	return managementOperation{contract.KVHTTPManagementOperation{Name: name, Title: title, Method: method, Path: path, BodyKind: body, Guard: guard, Options: strings.Fields(options), Write: method != "GET" && name != "audit-hash"}, ack, routing}
}

func managementSpec(name string) (managementOperation, bool) {
	for _, op := range managementOperations {
		if op.Name == name {
			return op, true
		}
	}
	return managementOperation{}, false
}

func ManagementRequest(input contract.KVHTTPManagementRequest) (Request, error) {
	op, ok := managementSpec(input.Operation)
	if !ok {
		return Request{}, ErrUnsupported
	}
	path := op.Path
	for name, value := range map[string]string{":kind": input.Kind, ":id": input.Identity} {
		if strings.Contains(path, name) {
			if !validText(value, 4096) || value == "" || !canonicalHTTPKey(value) || strings.Contains(value, "/") {
				return Request{}, ErrInvalid
			}
			path = strings.ReplaceAll(path, name, url.PathEscape(value))
		} else if value != "" {
			return Request{}, ErrInvalid
		}
	}
	r := Request{Method: op.Method, Path: path, Query: url.Values(input.Options).Clone(), Body: bytes.Clone(input.Body)}
	if err := validateManagement(r, op); err != nil {
		return Request{}, err
	}
	return r, nil
}

// Prefer literal routes (exact/match/check) before variable UUID/name routes.
func managementNativeSpec(r Request) (managementOperation, bool) {
	for pass := 0; pass < 2; pass++ {
		for _, op := range managementOperations {
			if strings.HasPrefix(op.Name, "probe-") || op.Name == "agent-self" {
				continue
			}
			variable := strings.Contains(op.Path, ":")
			if variable != (pass == 1) || r.Method != op.Method {
				continue
			}
			a, b := strings.Split(op.Path, "/"), strings.Split(r.Path, "/")
			if len(a) != len(b) {
				continue
			}
			match := true
			for i := range a {
				if strings.HasPrefix(a[i], ":") {
					v, e := url.PathUnescape(b[i])
					if e != nil || v == "" || !validText(v, 4096) || !canonicalHTTPKey(v) || strings.Contains(v, "/") || url.PathEscape(v) != b[i] {
						match = false
					}
				} else if a[i] != b[i] {
					match = false
				}
			}
			if match {
				return op, true
			}
		}
	}
	return managementOperation{}, false
}

func validateManagement(r Request, op managementOperation) error {
	if len(r.Body) > requestLimit {
		return ErrBounds
	}
	for key, values := range r.Query {
		if !slices.Contains(op.Options, key) || len(values) != 1 || !validText(values[0], 4096) {
			return ErrInvalid
		}
		v := values[0]
		switch key {
		case "index", "cas", "limit", "relay-factor":
			if !exactUint(v) || key == "relay-factor" && len(v) > 1 || key == "relay-factor" && v > "5" {
				return ErrInvalid
			}
		case "wan", "connect", "global", "local-only":
			if v != "true" && v != "false" && v != "" {
				return ErrInvalid
			}
		case "wait":
			if !r.Query.Has("index") || !validManagementWait(v) {
				return ErrInvalid
			}
		case "stale":
			if v != "" {
				return ErrInvalid
			}
		case "format":
			if v != "prometheus" {
				return ErrInvalid
			}
		}
	}
	if op.Name == "raft-remove" && (r.Query.Get("id") == "") == (r.Query.Get("address") == "") {
		return ErrInvalid
	}

	if strings.HasPrefix(op.Name, "intention-exact") || op.Name == "intention-check" {
		if r.Query.Get("source") == "" || r.Query.Get("destination") == "" {
			return ErrInvalid
		}
	}
	if op.Name == "intention-match" && (r.Query.Get("by") != "source" && r.Query.Get("by") != "destination" || r.Query.Get("name") == "") {
		return ErrInvalid
	}
	if op.Name == "event-fire" {
		if strings.HasPrefix(strings.TrimPrefix(r.Path, "/v1/event/fire/"), "_") || len(r.Body) > 512 {
			return ErrInvalid
		}
	}
	if op.Name == "peering-get" || op.Name == "peering-delete" {
		name, err := url.PathUnescape(strings.TrimPrefix(r.Path, "/v1/peering/"))
		if err != nil || !validPeeringName(name) {
			return ErrInvalid
		}
	}
	switch op.BodyKind {
	case "":
		if len(r.Body) != 0 {
			return ErrInvalid
		}
	case "bytes":
		if op.Name == "license-put" && len(r.Body) == 0 {
			return ErrInvalid
		}
	case "json":
		var fields map[string]json.RawMessage
		if jsondocument.Decode(r.Body, &fields, requestLimit) != nil || fields == nil {
			return ErrInvalid
		}
		// Scope and credentials are supplied by the original lease, except
		// operation-specific secret definitions such as Peering/Prepared Query.
		for key := range fields {
			if key == "Namespace" || key == "Partition" || key == "Datacenter" {
				return ErrUnsupported
			}
		}
		for _, raw := range fields {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return ErrInvalid
			}
		}
		if op.Name == "config-apply" {
			var kind, name string
			if json.Unmarshal(fields["Kind"], &kind) != nil || kind == "" || json.Unmarshal(fields["Name"], &name) != nil || name == "" || !r.Query.Has("cas") {
				return ErrInvalid
			}
		}
		if strings.HasPrefix(op.Name, "keyring-") {
			var key string
			if len(fields) != 1 || json.Unmarshal(fields["Key"], &key) != nil || key == "" {
				return ErrInvalid
			}
			decoded, e := base64.StdEncoding.Strict().DecodeString(key)
			if e != nil || base64.StdEncoding.EncodeToString(decoded) != key || len(decoded) != 16 && len(decoded) != 24 && len(decoded) != 32 {
				return ErrInvalid
			}
		}
		if err := validateManagementDefinition(r, op, fields); err != nil {
			return err
		}
	}
	if op.Name == "config-delete" && !r.Query.Has("cas") {
		return ErrInvalid
	}
	return nil
}

func managementGuard(cfg Config, guard string) bool {
	switch guard {
	case "autopilot":
		return cfg.AutopilotWrite
	case "raft":
		return cfg.RaftWrite
	case "keyring":
		return cfg.KeyringWrite
	case "license":
		return cfg.LicenseWrite
	}
	return true
}

func projectManagementResponse(r *Response, p Plan, op managementOperation) error {
	if r.Status != 200 {
		if !op.Write || r.Status == 401 || r.Status == 403 {
			r.Acknowledged = true
		}
		return &HTTPError{r.Status}
	}
	var v any
	switch op.ack {
	case "empty":
		if len(r.Body) != 0 {
			return ErrProtocol
		}
	case "bool":
		if !bytes.Equal(bytes.TrimSpace(r.Body), []byte("true")) && !bytes.Equal(bytes.TrimSpace(r.Body), []byte("false")) {
			return ErrProtocol
		}
		r.Acknowledged = true
		r.Succeeded = bytes.Equal(bytes.TrimSpace(r.Body), []byte("true"))
		if !r.Succeeded {
			return ErrConflict
		}
		return nil
	case "id":
		var body struct{ ID string }
		if jsondocument.Decode(r.Body, &body, responseLimit) != nil || !validID(body.ID) {
			return ErrProtocol
		}
		if op.Name == "intention-update" && !strings.HasSuffix(p.request.Path, "/"+body.ID) {
			return ErrProtocol
		}
	case "success":
		var body struct{ Success bool }
		if jsondocument.Decode(r.Body, &body, responseLimit) != nil || !body.Success {
			return ErrProtocol
		}
	case "object":
		var fields map[string]json.RawMessage
		if jsondocument.Decode(r.Body, &fields, responseLimit) != nil || fields == nil {
			return ErrProtocol
		}
	case "peering-token":
		var body struct{ PeeringToken string }
		if jsondocument.Decode(r.Body, &body, responseLimit) != nil || body.PeeringToken == "" {
			return ErrProtocol
		}
	case "event":
		var body struct{ ID, Name string }
		if jsondocument.Decode(r.Body, &body, responseLimit) != nil || !validID(body.ID) || url.PathEscape(body.Name) != strings.TrimPrefix(p.request.Path, "/v1/event/fire/") {
			return ErrProtocol
		}
	case "name":
		var body struct{ Name string }
		if jsondocument.Decode(r.Body, &body, responseLimit) != nil || body.Name == "" {
			return ErrProtocol
		}
		var input struct{ Name string }
		if json.Unmarshal(p.request.Body, &input) != nil || body.Name != input.Name {
			return ErrProtocol
		}
	default:
		if op.Name == "agent-metrics" && p.request.Query.Get("format") == "prometheus" {
			break
		}
		if jsondocument.Decode(r.Body, &v, responseLimit) != nil {
			return ErrProtocol
		}
	}
	r.Acknowledged, r.Succeeded = true, true
	return nil
}

func validManagementWait(v string) bool {
	d, e := time.ParseDuration(v)
	return e == nil && d > 0 && d <= 10*time.Minute
}

func validateManagementDefinition(r Request, op managementOperation, f map[string]json.RawMessage) error {
	text := func(key string) (string, bool) {
		var v string
		e := json.Unmarshal(f[key], &v)
		return v, e == nil && v != "" && validText(v, 4096)
	}
	switch op.Name {
	case "query-create", "query-update":
		var service map[string]json.RawMessage
		if json.Unmarshal(f["Service"], &service) != nil || service == nil {
			return ErrInvalid
		}
		var name string
		if json.Unmarshal(service["Service"], &name) != nil || name == "" {
			return ErrInvalid
		}
		var id string
		if raw, ok := f["ID"]; ok {
			if json.Unmarshal(raw, &id) != nil {
				return ErrInvalid
			}
		}
		if op.Name == "query-create" && id != "" || op.Name == "query-update" && (!validID(id) || !strings.HasSuffix(r.Path, "/"+id)) {
			return ErrInvalid
		}
	case "intention-create", "intention-update":
		if _, ok := text("SourceName"); !ok {
			return ErrInvalid
		}
		if _, ok := text("DestinationName"); !ok {
			return ErrInvalid
		}
		action, ok := text("Action")
		if !ok || action != "allow" && action != "deny" {
			return ErrInvalid
		}
		if raw, ok := f["ID"]; ok {
			var id string
			if json.Unmarshal(raw, &id) != nil || op.Name == "intention-create" && id != "" || op.Name == "intention-update" && !strings.HasSuffix(r.Path, "/"+id) {
				return ErrInvalid
			}
		}
	case "intention-exact-put":
		action, ok := text("Action")
		if !ok || action != "allow" && action != "deny" {
			if _, ok := f["Permissions"]; !ok {
				return ErrInvalid
			}
		}
	case "peering-token", "peering-establish":
		name, ok := text("PeerName")
		if !ok || !validPeeringName(name) {
			return ErrInvalid
		}
		if op.Name == "peering-establish" {
			if _, ok := text("PeeringToken"); !ok {
				return ErrInvalid
			}
		}
	case "namespace-create", "namespace-update", "partition-create", "partition-update":
		name, ok := text("Name")
		if !ok || strings.ContainsAny(name, "/ *?\\") {
			return ErrInvalid
		}
		if strings.HasSuffix(op.Name, "update") && !strings.HasSuffix(r.Path, "/"+url.PathEscape(name)) {
			return ErrInvalid
		}
	case "autopilot-update":
		for _, key := range []string{"CleanupDeadServers", "LastContactThreshold", "MaxTrailingLogs", "MinQuorum", "ServerStabilizationTime"} {
			if _, ok := f[key]; !ok {
				return ErrInvalid
			}
		}
		for _, key := range []string{"LastContactThreshold", "ServerStabilizationTime"} {
			v, ok := text(key)
			d, e := time.ParseDuration(v)
			if !ok || e != nil || d < 0 {
				return ErrInvalid
			}
		}
	case "audit-hash":
		if _, ok := text("Input"); !ok || len(f) != 1 {
			return ErrInvalid
		}
	}
	return nil
}

func validPeeringName(name string) bool {
	if name == "" || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, char := range name {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
			return false
		}
	}
	return true
}
