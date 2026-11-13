package consul

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

type healthTarget struct{ kind, name string }

func healthProjectionTarget(r contract.KVHTTPRequest) (healthTarget, bool) {
	if r.Method != "GET" || r.Body != nil {
		return healthTarget{}, false
	}
	for _, kind := range []string{"state", "node", "checks", "service"} {
		prefix := "/v1/health/" + kind + "/"
		if !strings.HasPrefix(r.Path, prefix) {
			continue
		}
		name, err := url.PathUnescape(strings.TrimPrefix(r.Path, prefix))
		if err != nil || name == "" || !validKey(name, false) || url.PathEscape(name) != strings.TrimPrefix(r.Path, prefix) {
			return healthTarget{}, false
		}
		if kind == "state" && !slices.Contains([]string{"any", "passing", "warning", "critical"}, name) {
			return healthTarget{}, false
		}
		return healthTarget{kind, name}, true
	}
	return healthTarget{}, false
}

func projectDocumentHealth(out *contract.KVResult) {
	h := out.HTTP
	target, ok := healthProjectionTarget(h.Request)
	if !ok || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded {
		return
	}
	list, err := decodeHealthProjection(h.Body, target)
	if err != nil {
		message := "Health projection unavailable; original HTTP body and acknowledgement retained: " + err.Error()
		if h.ProjectionError != "" {
			message = h.ProjectionError + "; " + message
		}
		h.ProjectionError, out.ProjectionError = message, message
		return
	}
	list.ProjectionComplete = true
	list.Complete = h.ProjectionError == "" && (h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered)
	h.Health = &list
}

type healthProjectionBudget struct {
	rows  int
	bytes int64
}

func (b *healthProjectionBudget) row() error {
	b.rows++
	if b.rows > entryLimit {
		return ErrBounds
	}
	return nil
}
func (b *healthProjectionBudget) add(n int64) error {
	b.bytes += n
	if b.bytes > projectionBudget {
		return ErrBounds
	}
	return nil
}

func decodeHealthProjection(raw []byte, target healthTarget) (contract.KVHTTPHealthList, error) {
	var valid sessionProjectionJSON
	if err := jsondocument.Decode(raw, &valid, responseLimit); err != nil {
		return contract.KVHTTPHealthList{}, errors.Join(ErrProtocol, err)
	}
	list := contract.KVHTTPHealthList{Kind: "checks", Checks: []contract.KVHTTPHealthCheck{}}
	b := &healthProjectionBudget{bytes: 128 + int64(len("service-instances"))}
	if target.kind != "service" {
		checks, err := decodeHealthChecks(raw, b, target)
		if err != nil {
			return contract.KVHTTPHealthList{}, err
		}
		list.Checks = checks
		return list, nil
	}
	list.Kind, list.Checks, list.Instances = "service-instances", nil, []contract.KVHTTPHealthInstance{}
	seen := map[healthIdentity]bool{}
	err := sessionProjectionArray(raw, entryLimit, func(raw json.RawMessage) error {
		if err := b.row(); err != nil {
			return err
		}
		fields, present, nulls, err := sessionProjectionFields(raw)
		if err != nil {
			return err
		}
		i := contract.KVHTTPHealthInstance{PresentFields: present, NullFields: nulls}
		i.Node, err = decodeHealthNode(fields["Node"])
		if err != nil {
			return err
		}
		i.Service, err = decodeHealthService(fields["Service"])
		if err != nil {
			return err
		}
		if i.Service.Service != target.name {
			return ErrProtocol
		}
		id := healthQualifiedIdentity(i.Node.Node, i.Service.ID, i.Service.Namespace, i.Service.Partition, i.Service.PresentFields, i.Service.NullFields)
		if seen[id] {
			return ErrProtocol
		}
		seen[id] = true
		if v, ok := fields["Checks"]; ok && !slices.Contains(nulls, "Checks") {
			i.Checks, err = decodeHealthChecks(v, b, healthTarget{kind: "node", name: i.Node.Node})
			if err != nil {
				return err
			}
		}
		// Nested checks have already been charged individually.
		withoutChecks := i
		withoutChecks.Checks = nil
		if err := b.add(healthInstanceCost(withoutChecks)); err != nil {
			return err
		}
		list.Instances = append(list.Instances, i)
		return nil
	})
	if err != nil {
		return contract.KVHTTPHealthList{}, err
	}
	return list, nil
}

type healthIdentity struct {
	node, id, namespace, partition                                   string
	namespacePresent, namespaceNull, partitionPresent, partitionNull bool
}

func healthQualifiedIdentity(node, id, namespace, partition string, present, nulls []string) healthIdentity {
	return healthIdentity{node, id, namespace, partition, slices.Contains(present, "Namespace"), slices.Contains(nulls, "Namespace"), slices.Contains(present, "Partition"), slices.Contains(nulls, "Partition")}
}
func decodeHealthChecks(raw []byte, b *healthProjectionBudget, target healthTarget) ([]contract.KVHTTPHealthCheck, error) {
	rows := []contract.KVHTTPHealthCheck{}
	seen := map[healthIdentity]bool{}
	err := sessionProjectionArray(raw, entryLimit, func(raw json.RawMessage) error {
		if err := b.row(); err != nil {
			return err
		}
		row, err := decodeHealthCheck(raw)
		if err != nil {
			return err
		}
		if target.kind == "node" && row.Node != target.name || target.kind == "checks" && row.ServiceName != target.name || target.kind == "state" && target.name != "any" && row.Status != target.name {
			return ErrProtocol
		}
		id := healthQualifiedIdentity(row.Node, row.CheckID, row.Namespace, row.Partition, row.PresentFields, row.NullFields)
		if seen[id] {
			return ErrProtocol
		}
		seen[id] = true
		if err := b.add(healthCheckCost(row)); err != nil {
			return err
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func healthStrings(fields map[string]json.RawMessage, nulls []string, targets map[string]*string) error {
	for key, p := range targets {
		if v, ok := fields[key]; ok && !slices.Contains(nulls, key) {
			if json.Unmarshal(v, p) != nil {
				return ErrProtocol
			}
		}
	}
	return nil
}
func healthUints(fields map[string]json.RawMessage, nulls []string, targets map[string]*uint64) error {
	for key, p := range targets {
		if v, ok := fields[key]; ok && !slices.Contains(nulls, key) {
			if json.Unmarshal(v, p) != nil {
				return ErrProtocol
			}
		}
	}
	return nil
}
func healthOptionalStringAlias(fields map[string]json.RawMessage, canonical, alias string, dst *string) error {
	a, ap := fields[canonical]
	b, bp := fields[alias]
	if !bp {
		return nil
	}
	an, bn := bytes.Equal(bytes.TrimSpace(a), []byte("null")), bytes.Equal(bytes.TrimSpace(b), []byte("null"))
	var av, bv string
	if ap && !an && json.Unmarshal(a, &av) != nil || !bn && json.Unmarshal(b, &bv) != nil {
		return ErrProtocol
	}
	if ap && (an != bn || av != bv) {
		return ErrProtocol
	}
	if !bn {
		*dst = bv
	}
	return nil
}
func healthAliasID(fields map[string]json.RawMessage, canonical, alias string) (string, error) {
	var id, other string
	v, present := fields[canonical]
	if present {
		if json.Unmarshal(v, &id) != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return "", ErrProtocol
		}
	}
	if v, ok := fields[alias]; ok {
		if json.Unmarshal(v, &other) != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) || present && id != other {
			return "", ErrProtocol
		}
		id = other
	}
	if !validKey(id, false) {
		return "", ErrProtocol
	}
	return id, nil
}
func healthStringList(v json.RawMessage) ([]string, error) {
	rows := []string{}
	err := sessionProjectionArray(v, entryLimit, func(v json.RawMessage) error {
		var text string
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || json.Unmarshal(v, &text) != nil {
			return ErrProtocol
		}
		rows = append(rows, text)
		return nil
	})
	return rows, err
}
func healthStringMap(v json.RawMessage) (map[string]string, error) {
	fields, _, nulls, err := sessionProjectionFields(v)
	if err != nil || len(nulls) != 0 {
		return nil, ErrProtocol
	}
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		var text string
		if json.Unmarshal(v, &text) != nil {
			return nil, ErrProtocol
		}
		out[k] = text
	}
	return out, nil
}
func healthDuration(v json.RawMessage) (contract.KVHTTPHealthDuration, error) {
	v = bytes.TrimSpace(v)
	d := contract.KVHTTPHealthDuration{}
	if len(v) == 0 {
		return d, ErrProtocol
	}
	if v[0] == '"' {
		if json.Unmarshal(v, &d.Text) != nil {
			return d, ErrProtocol
		}
		return d, nil
	}
	var n uint64
	if json.Unmarshal(v, &n) != nil {
		return d, ErrProtocol
	}
	d.Nanoseconds = &n
	return d, nil
}
func decodeHealthCheck(raw json.RawMessage) (contract.KVHTTPHealthCheck, error) {
	fields, present, nulls, err := sessionProjectionFields(raw)
	c := contract.KVHTTPHealthCheck{PresentFields: present, NullFields: nulls}
	if err != nil {
		return c, err
	}
	c.CheckID, err = healthAliasID(fields, "CheckID", "CheckId")
	if err != nil {
		return c, err
	}
	if err = healthStrings(fields, nulls, map[string]*string{"Node": &c.Node, "Name": &c.Name, "Status": &c.Status, "Notes": &c.Notes, "Output": &c.Output, "ServiceID": &c.ServiceID, "ServiceName": &c.ServiceName, "Type": &c.Type, "Namespace": &c.Namespace, "Partition": &c.Partition}); err != nil {
		return c, err
	}
	if err = healthOptionalStringAlias(fields, "ServiceID", "ServiceId", &c.ServiceID); err != nil {
		return c, err
	}
	if !validKey(c.Node, false) {
		return c, ErrProtocol
	}
	if err = healthUints(fields, nulls, map[string]*uint64{"ExposedPort": &c.ExposedPort, "CreateIndex": &c.CreateIndex, "ModifyIndex": &c.ModifyIndex}); err != nil || c.ExposedPort > 65535 {
		return c, ErrProtocol
	}
	for key, ptr := range map[string]*contract.KVHTTPHealthDuration{"Interval": &c.Interval, "Timeout": &c.Timeout} {
		if v, ok := fields[key]; ok && !slices.Contains(nulls, key) {
			*ptr, err = healthDuration(v)
			if err != nil {
				return c, err
			}
		}
	}
	if v, ok := fields["ServiceTags"]; ok && !slices.Contains(nulls, "ServiceTags") {
		c.ServiceTags, err = healthStringList(v)
		if err != nil {
			return c, err
		}
	}
	if v, ok := fields["Definition"]; ok && !slices.Contains(nulls, "Definition") {
		def, p, n, err := sessionProjectionFields(v)
		if err != nil {
			return c, err
		}
		c.Definition.PresentFields, c.Definition.NullFields = p, n
		if err = healthStrings(def, n, map[string]*string{"HTTP": &c.Definition.HTTP, "TCP": &c.Definition.TCP, "GRPC": &c.Definition.GRPC}); err != nil {
			return c, err
		}
		for key, ptr := range map[string]*contract.KVHTTPHealthDuration{"Interval": &c.Definition.Interval, "Timeout": &c.Definition.Timeout, "TTL": &c.Definition.TTL} {
			if v, ok := def[key]; ok && !slices.Contains(n, key) {
				*ptr, err = healthDuration(v)
				if err != nil {
					return c, err
				}
			}
		}
	}
	return c, nil
}
func decodeHealthNode(raw json.RawMessage) (contract.KVHTTPHealthNode, error) {
	fields, present, nulls, err := sessionProjectionFields(raw)
	n := contract.KVHTTPHealthNode{PresentFields: present, NullFields: nulls}
	if err != nil {
		return n, err
	}
	if err = healthStrings(fields, nulls, map[string]*string{"ID": &n.ID, "Node": &n.Node, "Address": &n.Address, "Datacenter": &n.Datacenter, "Namespace": &n.Namespace, "Partition": &n.Partition}); err != nil {
		return n, err
	}
	if err = healthOptionalStringAlias(fields, "ID", "Id", &n.ID); err != nil {
		return n, err
	}
	if !validKey(n.Node, false) {
		return n, ErrProtocol
	}
	if err = healthUints(fields, nulls, map[string]*uint64{"CreateIndex": &n.CreateIndex, "ModifyIndex": &n.ModifyIndex}); err != nil {
		return n, err
	}
	for key, ptr := range map[string]*map[string]string{"TaggedAddresses": &n.TaggedAddresses, "Meta": &n.Meta, "NodeMeta": &n.NodeMeta} {
		if v, ok := fields[key]; ok && !slices.Contains(nulls, key) {
			*ptr, err = healthStringMap(v)
			if err != nil {
				return n, err
			}
		}
	}
	return n, nil
}
func decodeHealthService(raw json.RawMessage) (contract.KVHTTPHealthService, error) {
	fields, present, nulls, err := sessionProjectionFields(raw)
	s := contract.KVHTTPHealthService{PresentFields: present, NullFields: nulls}
	if err != nil {
		return s, err
	}
	s.ID, err = healthAliasID(fields, "ID", "Id")
	if err != nil {
		return s, err
	}
	if err = healthStrings(fields, nulls, map[string]*string{"Service": &s.Service, "Address": &s.Address, "Kind": &s.Kind, "Namespace": &s.Namespace, "Partition": &s.Partition}); err != nil {
		return s, err
	}
	if !validKey(s.Service, false) {
		return s, ErrProtocol
	}
	if err = healthUints(fields, nulls, map[string]*uint64{"Port": &s.Port, "CreateIndex": &s.CreateIndex, "ModifyIndex": &s.ModifyIndex}); err != nil || s.Port > 65535 {
		return s, ErrProtocol
	}
	if v, ok := fields["EnableTagOverride"]; ok && !slices.Contains(nulls, "EnableTagOverride") {
		if json.Unmarshal(v, &s.EnableTagOverride) != nil {
			return s, ErrProtocol
		}
	}
	if v, ok := fields["Tags"]; ok && !slices.Contains(nulls, "Tags") {
		s.Tags, err = healthStringList(v)
		if err != nil {
			return s, err
		}
	}
	if v, ok := fields["Meta"]; ok && !slices.Contains(nulls, "Meta") {
		s.Meta, err = healthStringMap(v)
		if err != nil {
			return s, err
		}
	}
	if v, ok := fields["TaggedAddresses"]; ok && !slices.Contains(nulls, "TaggedAddresses") {
		addresses, _, _, err := sessionProjectionFields(v)
		if err != nil {
			return s, err
		}
		s.TaggedAddresses = make(map[string]contract.KVHTTPHealthServiceAddress, len(addresses))
		for key, v := range addresses {
			f, p, n, err := sessionProjectionFields(v)
			if err != nil {
				return s, err
			}
			a := contract.KVHTTPHealthServiceAddress{PresentFields: p, NullFields: n}
			if err = healthStrings(f, n, map[string]*string{"Address": &a.Address}); err != nil {
				return s, err
			}
			if err = healthUints(f, n, map[string]*uint64{"Port": &a.Port}); err != nil || a.Port > 65535 {
				return s, ErrProtocol
			}
			s.TaggedAddresses[key] = a
		}
	}
	if v, ok := fields["Weights"]; ok && !slices.Contains(nulls, "Weights") {
		f, p, n, err := sessionProjectionFields(v)
		if err != nil {
			return s, err
		}
		s.Weights.PresentFields, s.Weights.NullFields = p, n
		if err = healthUints(f, n, map[string]*uint64{"Passing": &s.Weights.Passing, "Warning": &s.Weights.Warning}); err != nil {
			return s, err
		}
	}
	return s, nil
}

func healthFieldsCost(values ...[]string) int64 {
	n := int64(0)
	for _, rows := range values {
		for _, v := range rows {
			n += 24 + int64(len(v))
		}
	}
	return n
}
func healthStringsCost(values ...string) int64 {
	n := int64(0)
	for _, v := range values {
		n += 32 + int64(len(v))
	}
	return n
}
func healthMapCost(m map[string]string) int64 {
	n := int64(64)
	for k, v := range m {
		n += 96 + int64(len(k)+len(v))
	}
	return n
}
func healthCheckCost(c contract.KVHTTPHealthCheck) int64 {
	d := c.Definition
	return 608 + healthFieldsCost(c.PresentFields, c.NullFields, c.ServiceTags, d.PresentFields, d.NullFields) + healthStringsCost(c.Node, c.CheckID, c.Name, c.Status, c.Notes, c.Output, c.ServiceID, c.ServiceName, c.Type, c.Namespace, c.Partition, c.Interval.Text, c.Timeout.Text, d.HTTP, d.TCP, d.GRPC, d.Interval.Text, d.Timeout.Text, d.TTL.Text)
}
func healthInstanceCost(i contract.KVHTTPHealthInstance) int64 {
	n, s := i.Node, i.Service
	cost := int64(1024) + healthFieldsCost(i.PresentFields, i.NullFields, n.PresentFields, n.NullFields, s.PresentFields, s.NullFields, s.Tags, s.Weights.PresentFields, s.Weights.NullFields) + healthStringsCost(n.ID, n.Node, n.Address, n.Datacenter, n.Namespace, n.Partition, s.ID, s.Service, s.Address, s.Kind, s.Namespace, s.Partition) + healthMapCost(n.Meta) + healthMapCost(n.NodeMeta) + healthMapCost(n.TaggedAddresses) + healthMapCost(s.Meta)
	for key, a := range s.TaggedAddresses {
		cost += 128 + int64(len(key)+len(a.Address)) + healthFieldsCost(a.PresentFields, a.NullFields)
	}
	for _, c := range i.Checks {
		cost += healthCheckCost(c)
	}
	return cost
}
func healthProjectionCost(list contract.KVHTTPHealthList) int64 {
	n := int64(128 + len(list.Kind))
	for _, c := range list.Checks {
		n += healthCheckCost(c)
	}
	for _, i := range list.Instances {
		n += healthInstanceCost(i)
	}
	return n
}
