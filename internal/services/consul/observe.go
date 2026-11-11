package consul

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
)

// Agent identity is an observation of the contacted agent, not an incarnation.
// KV-only tokens can lack agent:read. Their version/agent identity stays unavailable.
type Identity struct {
	Version, Revision, NodeID, NodeName, Datacenter string
	Available                                       bool
}
type OpenEvidence struct {
	Identity           Identity
	Self, Leader, Keys Response
}

func (e OpenEvidence) Clone() OpenEvidence {
	e.Self = e.Self.Clone()
	e.Leader = e.Leader.Clone()
	e.Keys = e.Keys.Clone()
	return e
}
func Open(ctx context.Context, cfg Config, token string) (*Client, OpenEvidence, error) {
	c, e := New(cfg, token)
	if e != nil {
		return nil, OpenEvidence{}, e
	}
	evidence, e := c.Observe(ctx)
	if e != nil {
		_ = c.Close()
		return nil, evidence, e
	}
	return c, evidence, nil
}
func (c *Client) Observe(ctx context.Context) (OpenEvidence, error) {
	v := OpenEvidence{}
	r, e := c.Do(ctx, Request{Method: "GET", Path: "/v1/agent/self"})
	v.Self = r.Clone()
	if e == nil {
		var body struct {
			Config struct{ Version, Revision, NodeID, NodeName, Datacenter string }
		}
		if json.Unmarshal(r.Body, &body) != nil || !validVersion(body.Config.Version) || !validID(body.Config.NodeID) || !validText(body.Config.NodeName, 4096) || body.Config.NodeName == "" || !validText(body.Config.Datacenter, 255) || body.Config.Datacenter == "" {
			return v, ErrProtocol
		}
		v.Identity = Identity{body.Config.Version, body.Config.Revision, body.Config.NodeID, body.Config.NodeName, body.Config.Datacenter, true}
	} else {
		var status *HTTPError
		if !errors.As(e, &status) || status.Status != 403 || !r.Captured || !r.Acknowledged {
			return v, e
		}
	}
	r, e = c.Do(ctx, Request{Method: "GET", Path: "/v1/status/leader"})
	v.Leader = r.Clone()
	if e != nil {
		return v, e
	}
	var address string
	if json.Unmarshal(r.Body, &address) != nil {
		return v, ErrProtocol
	}
	if address != "" {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return v, ErrProtocol
		}
		n, e := strconv.ParseUint(port, 10, 16)
		if e != nil || n == 0 {
			return v, ErrProtocol
		}
	}
	request, e := KeyListRequest(c.config.Prefix, true)
	if e != nil {
		return v, e
	}
	r, e = c.Do(ctx, request)
	v.Keys = r.Clone()
	if e != nil {
		return v, e
	}
	if r.Kind != "txn" && (r.Metadata.Index == nil || *r.Metadata.Index == 0) {
		return v, ErrProtocol
	}
	return v, nil
}
func validVersion(v string) bool {
	core := strings.SplitN(v, "+", 2)[0]
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		n, e := strconv.ParseUint(p, 10, 32)
		if e != nil || strconv.FormatUint(n, 10) != p {
			return false
		}
	}
	major, _ := strconv.Atoi(parts[0])
	return major == 1 || major == 2
}
