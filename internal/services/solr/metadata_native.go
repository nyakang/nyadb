package solr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrChanged = &metadataChanged{}

type metadataChanged struct{}

func (*metadataChanged) Error() string {
	return "Solr target or schema/config observation changed; refresh explicitly"
}

// Target preserves native resolution and a revocable observation fingerprint.
// Neither a core startTime nor ZooKeeper czxid proves a permanent object UUID.
type Target struct {
	Name, Kind, Fingerprint string
	Members                 []string
	Definition              json.RawMessage
}
type MemberMetadata struct {
	Name           string
	Schema, Config json.RawMessage
}
type TargetMetadata struct {
	Target      Target
	Members     []MemberMetadata
	Fingerprint string
}

func validTargetName(name string) bool {
	return name != "" && len(name) <= 512 && utf8.ValidString(name) && name != "." && name != ".." && !strings.ContainsAny(name, "/\\%,?#:") && strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
func canonical(raw []byte) (json.RawMessage, error) {
	var value any
	if err := decodeJSON(raw, &value); err != nil {
		return nil, err
	}
	result, err := json.Marshal(value)
	return result, err
}
func fingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func makeTarget(name, kind string, members []string, native any) (Target, error) {
	raw, err := json.Marshal(native)
	if err != nil {
		return Target{}, ErrProtocol
	}
	raw, err = canonical(raw)
	if err != nil {
		return Target{}, err
	}
	return Target{Name: name, Kind: kind, Members: append([]string(nil), members...), Definition: raw, Fingerprint: fingerprint(raw)}, nil
}
func (c *Client) nativeJSON(ctx context.Context, path string, target any) error {
	r, err := c.Do(ctx, Request{Method: "GET", Path: path})
	if err != nil {
		return err
	}
	var envelope struct {
		Header *struct{ Status int } `json:"responseHeader"`
	}
	if decodeJSON(r.Body, &envelope) != nil || envelope.Header == nil || envelope.Header.Status != 0 {
		return ErrProtocol
	}
	return decodeJSON(r.Body, target)
}
func (c *Client) ResolveTargets(ctx context.Context) ([]Target, error) {
	if c.identity.Mode == "std" {
		return c.coreTargets(ctx)
	}
	return c.cloudTargets(ctx)
}
func (c *Client) coreTargets(ctx context.Context) ([]Target, error) {
	type core struct {
		Name        string `json:"name"`
		InstanceDir string `json:"instanceDir"`
		DataDir     string `json:"dataDir"`
		Config      string `json:"config"`
		Schema      string `json:"schema"`
		StartTime   string `json:"startTime"`
		Cloud       struct {
			Collection string `json:"collection"`
		} `json:"cloud"`
	}
	var status struct {
		Status   map[string]core            `json:"status"`
		Failures map[string]json.RawMessage `json:"initFailures"`
	}
	if err := c.nativeJSON(ctx, "/admin/cores?action=STATUS&indexInfo=false&wt=json", &status); err != nil {
		return nil, err
	}
	if status.Status == nil || len(status.Status) > 4096 || len(status.Failures) != 0 {
		return nil, ErrProtocol
	}
	targets := make([]Target, 0, len(status.Status))
	for name, v := range status.Status {
		if !validTargetName(name) || name != v.Name || v.InstanceDir == "" || v.DataDir == "" || v.Config == "" || v.Schema == "" {
			return nil, ErrProtocol
		}
		if _, err := time.Parse(time.RFC3339Nano, v.StartTime); err != nil {
			return nil, ErrProtocol
		}
		t, err := makeTarget(name, "core", []string{name}, v)
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return finishTargets(targets)
}
func finishTargets(targets []Target) ([]Target, error) {
	total := 0
	for _, t := range targets {
		total += len(t.Definition)
		if total > responseLimit {
			return nil, ErrBounds
		}
	}
	sortTargets(targets)
	return targets, nil
}
func sortTargets(targets []Target) {
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Kind == targets[j].Kind {
			return targets[i].Name < targets[j].Name
		}
		return targets[i].Kind < targets[j].Kind
	})
}
func (c *Client) cloudTargets(ctx context.Context) ([]Target, error) {
	var cluster struct {
		Cluster *struct {
			Collections map[string]json.RawMessage `json:"collections"`
		} `json:"cluster"`
	}
	if err := c.nativeJSON(ctx, "/admin/collections?action=CLUSTERSTATUS&wt=json", &cluster); err != nil {
		return nil, err
	}
	if cluster.Cluster == nil || cluster.Cluster.Collections == nil || len(cluster.Cluster.Collections) > 4096 {
		return nil, ErrProtocol
	}
	var aliases struct {
		Aliases    map[string]string          `json:"aliases"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := c.nativeJSON(ctx, "/admin/collections?action=LISTALIASES&wt=json", &aliases); err != nil {
		return nil, err
	}
	if aliases.Aliases == nil || len(aliases.Aliases) > 4096 {
		return nil, ErrProtocol
	}
	localCores, err := c.coreTargets(ctx)
	if err != nil {
		return nil, err
	}
	localByCollection := map[string][]json.RawMessage{}
	for _, core := range localCores {
		var v struct {
			Cloud struct {
				Collection string `json:"collection"`
			} `json:"cloud"`
		}
		if decodeJSON(core.Definition, &v) != nil {
			return nil, ErrProtocol
		}
		if v.Cloud.Collection != "" {
			localByCollection[v.Cloud.Collection] = append(localByCollection[v.Cloud.Collection], core.Definition)
		}
	}
	byName := map[string]Target{}
	targets := make([]Target, 0, len(cluster.Cluster.Collections)+len(aliases.Aliases))
	total := 0
	for name, state := range cluster.Cluster.Collections {
		if !validTargetName(name) {
			return nil, ErrProtocol
		}
		var v struct {
			ConfigName string                     `json:"configName"`
			Created    int64                      `json:"creationTimeMillis"`
			Shards     map[string]json.RawMessage `json:"shards"`
			Version    *int64                     `json:"znodeVersion"`
		}
		if decodeJSON(state, &v) != nil || v.ConfigName == "" || v.Created <= 0 || v.Version == nil || *v.Version < 0 || len(v.Shards) == 0 {
			return nil, ErrProtocol
		}
		// Only extract the collection znode's creation observation, never ZK data or
		// configuration credentials. This endpoint has no responseHeader in 9.9.0.
		r, err := c.Do(ctx, Request{Method: "GET", Path: "/admin/zookeeper?path=" + url.QueryEscape("/collections/"+name) + "&detail=true&wt=json"})
		if err != nil {
			return nil, err
		}
		var zk struct {
			Znode *struct {
				Path string `json:"path"`
				Prop *struct {
					CTime string      `json:"ctime"`
					CZXID json.Number `json:"czxid"`
				} `json:"prop"`
			} `json:"znode"`
		}
		if decodeJSON(r.Body, &zk) != nil || zk.Znode == nil || zk.Znode.Prop == nil || zk.Znode.Path != "/collections/"+name || zk.Znode.Prop.CTime == "" {
			return nil, ErrProtocol
		}
		if n, err := zk.Znode.Prop.CZXID.Int64(); err != nil || n <= 0 {
			return nil, ErrProtocol
		}
		t, err := makeTarget(name, "collection", []string{name}, struct {
			State      json.RawMessage
			Creation   any
			LocalCores []json.RawMessage
		}{state, zk.Znode.Prop, localByCollection[name]})
		if err != nil {
			return nil, err
		}
		total += len(t.Definition)
		if total > responseLimit {
			return nil, ErrBounds
		}
		byName[name] = t
		targets = append(targets, t)
	}
	for name, resolution := range aliases.Aliases {
		if !validTargetName(name) {
			return nil, ErrProtocol
		}
		if _, exists := byName[name]; exists {
			return nil, ErrProtocol
		}
		members := strings.Split(resolution, ",")
		if len(members) == 0 || len(members) > 4096 {
			return nil, ErrProtocol
		}
		definitions := make([]json.RawMessage, 0, len(members))
		aliasBytes := 0
		seen := map[string]bool{}
		for _, member := range members {
			t, ok := byName[member]
			if !ok || seen[member] {
				return nil, ErrProtocol
			}
			seen[member] = true
			aliasBytes += len(t.Definition)
			if total+aliasBytes > responseLimit {
				return nil, ErrBounds
			}
			definitions = append(definitions, t.Definition)
		}
		t, err := makeTarget(name, "alias", members, struct {
			Name        string
			Members     []string
			Properties  json.RawMessage
			Collections []json.RawMessage
		}{name, members, aliases.Properties[name], definitions})
		if err != nil {
			return nil, err
		}
		total += len(t.Definition)
		if total > responseLimit {
			return nil, ErrBounds
		}
		targets = append(targets, t)
	}
	return finishTargets(targets)
}
func findTarget(targets []Target, name, kind string) (Target, error) {
	for _, t := range targets {
		if t.Name == name && t.Kind == kind {
			return t, nil
		}
	}
	return Target{}, ErrChanged
}
func (c *Client) readMembers(ctx context.Context, t Target) ([]MemberMetadata, error) {
	members := make([]MemberMetadata, 0, len(t.Members))
	total := 0
	for _, name := range t.Members {
		v := MemberMetadata{Name: name}
		for _, endpoint := range []string{"schema", "config"} {
			var envelope map[string]json.RawMessage
			if err := c.nativeJSON(ctx, "/"+url.PathEscape(name)+"/"+endpoint+"?wt=json", &envelope); err != nil {
				return nil, err
			}
			raw, ok := envelope[endpoint]
			var object map[string]json.RawMessage
			if decodeJSON(raw, &object) != nil || object == nil {
				return nil, ErrProtocol
			}
			if !ok || len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
				return nil, ErrProtocol
			}
			raw, err := canonical(raw)
			if err != nil {
				return nil, err
			}
			total += len(raw)
			if total > responseLimit {
				return nil, ErrBounds
			}
			if endpoint == "schema" {
				v.Schema = raw
			} else {
				v.Config = raw
			}
		}
		members = append(members, v)
	}
	return members, nil
}

// ReadTargetMetadata compares complete canonical schema/config and native target
// observations twice. Cloud schemas come from representative routed replicas;
// this is not a distributed consensus check or an atomic snapshot.
func (c *Client) ReadTargetMetadata(ctx context.Context, name, kind string) (TargetMetadata, error) {
	if !validTargetName(name) {
		return TargetMetadata{}, ErrInvalid
	}
	targets, err := c.ResolveTargets(ctx)
	if err != nil {
		return TargetMetadata{}, err
	}
	t, err := findTarget(targets, name, kind)
	if err != nil {
		return TargetMetadata{}, err
	}
	members, err := c.readMembers(ctx, t)
	if err != nil {
		return TargetMetadata{}, err
	}
	first, _ := json.Marshal(members)
	for pass := 0; pass < 2; pass++ {
		targets, err = c.ResolveTargets(ctx)
		if err != nil {
			return TargetMetadata{}, err
		}
		observed, err := findTarget(targets, name, kind)
		if err != nil || observed.Fingerprint != t.Fingerprint {
			return TargetMetadata{}, ErrChanged
		}
		if pass == 0 {
			again, e := c.readMembers(ctx, t)
			if e != nil {
				return TargetMetadata{}, e
			}
			second, _ := json.Marshal(again)
			if !bytes.Equal(first, second) {
				return TargetMetadata{}, ErrChanged
			}
		}
	}
	raw, _ := json.Marshal(struct {
		Target  Target
		Members []MemberMetadata
	}{t, members})
	return TargetMetadata{Target: t, Members: members, Fingerprint: fingerprint(raw)}, nil
}
func sameTargets(a, b []Target) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Kind != b[i].Kind || a[i].Fingerprint != b[i].Fingerprint {
			return false
		}
	}
	return true
}
