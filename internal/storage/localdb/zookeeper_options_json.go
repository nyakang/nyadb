package localdb

import (
	"encoding/json"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func decodeZooKeeperSavedOptions(p *connections.Profile, raw string) error {
	var root map[string]json.RawMessage
	var envelope engineOptionsEnvelope
	if jsondocument.Decode([]byte(raw), &root, 128<<10) != nil || len(root) != 3 || root["version"] == nil || root["kind"] == nil || root["zookeeper"] == nil || jsondocument.Decode([]byte(raw), &envelope, 128<<10) != nil || envelope.Version != 1 || envelope.Kind != p.Kind || envelope.ZooKeeper == nil {
		return connections.ErrInvalid
	}
	var options map[string]json.RawMessage
	if jsondocument.Decode(root["zookeeper"], &options, 128<<10) != nil {
		return connections.ErrInvalid
	}
	allowed := map[string]bool{}
	for _, k := range []string{"version", "additional_servers", "chroot", "namespace", "prefix", "auth", "session_timeout_ms", "connection_timeout_ms", "max_buffer_size", "max_documents", "ca_file", "client_cert_file", "client_key_file", "driver_version", "driver_sha256", "driver_directory"} {
		allowed[k] = true
	}
	for k := range options {
		if !allowed[k] || string(options[k]) == "null" {
			return connections.ErrInvalid
		}
	}
	for _, k := range []string{"version", "prefix", "auth", "session_timeout_ms", "connection_timeout_ms", "max_buffer_size", "max_documents"} {
		if options[k] == nil {
			return connections.ErrInvalid
		}
	}
	p.ZooKeeperOptions = *envelope.ZooKeeper
	checked, err := connections.NormalizeProfile(*p)
	*p = checked
	return err
}
