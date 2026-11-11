package consul

import (
	"context"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func OpenSession(ctx context.Context, p connections.Profile, token string) (contract.Session, error) {
	cfg, err := connections.ConsulProfileConfig(p)
	if err != nil {
		return nil, err
	}
	if err = connections.ValidateConsulCredential(cfg, token, true); err != nil {
		return nil, err
	}
	o := cfg.Options
	session, err := OpenNativeSession(ctx, p.ID, Config{Endpoint: cfg.Endpoint, Auth: o.Auth, Datacenter: o.Datacenter, Prefix: o.Prefix, Consistency: o.Consistency,
		CAFile: o.CAFile, ServerName: o.ServerName, ClientCertFile: o.ClientCertFile, ClientKeyFile: o.ClientKeyFile,
		ConnectTimeout: time.Duration(o.ConnectTimeoutSeconds) * time.Second, RequestTimeout: time.Duration(o.RequestTimeoutSeconds) * time.Second}, token)
	if err != nil {
		return nil, err
	}
	return session, nil
}
