package etcd

import (
	"context"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// OpenProfile binds the pure public snapshot to its original native channel.
// It does not register a runtime session or publish a generation.
func OpenProfile(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	cfg, err := connections.EtcdProfileConfig(p)
	if err != nil {
		return nil, err
	}
	if err = connections.ValidateEtcdCredential(cfg, secret, true); err != nil {
		return nil, err
	}
	prefix, err := connections.EtcdPrefixBytes(cfg.Options)
	if err != nil {
		return nil, err
	}
	auth := cfg.Options.Auth
	if auth == "certificate" {
		auth = "none"
	}
	o := cfg.Options
	return Open(ctx, Config{Endpoint: cfg.Endpoint, Auth: auth, Username: cfg.Username, CAFile: o.CAFile, ServerName: o.ServerName, ClientCertFile: o.ClientCertFile, ClientKeyFile: o.ClientKeyFile, ProbeKey: prefix, DiscoverAccess: true, BrowsePrefix: prefix, ConnectTimeout: time.Duration(o.ConnectTimeoutSeconds) * time.Second, RequestTimeout: time.Duration(o.RequestTimeoutSeconds) * time.Second}, secret)
}
