package consul

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

type catalogTarget struct{ kind, name string }

func catalogProjectionTarget(r contract.KVHTTPRequest) (catalogTarget, bool) {
	if r.Method != "GET" || r.Body != nil {
		return catalogTarget{}, false
	}
	request := Request{Method: r.Method, Path: r.Path, Query: url.Values(r.Query)}
	if _, _, _, err := validateRequest(request); err != nil {
		return catalogTarget{}, false
	}
	kind := catalogReadKind(r.Path)
	if kind == "" {
		return catalogTarget{}, false
	}
	t := catalogTarget{kind: kind}
	if kind == "node" || kind == "service" {
		encoded := strings.TrimPrefix(r.Path, "/v1/catalog/"+kind+"/")
		name, err := url.PathUnescape(encoded)
		if err != nil || !validKey(name, false) || !canonicalHTTPKey(name) || url.PathEscape(name) != encoded {
			return catalogTarget{}, false
		}
		t.name = name
	}
	return t, true
}

func projectDocumentCatalog(out *contract.KVResult) {
	h := out.HTTP
	target, ok := catalogProjectionTarget(h.Request)
	if !ok || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown {
		return
	}
	list, err := decodeCatalogProjection(h.Body, target)
	if err != nil {
		message := "Catalog projection unavailable; original HTTP body and acknowledgement retained: " + err.Error()
		if h.ProjectionError != "" {
			message = h.ProjectionError + "; " + message
		}
		h.ProjectionError, out.ProjectionError = message, message
		return
	}
	list.ProjectionComplete = true
	list.Complete = h.ProjectionError == "" && (h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered)
	h.Catalog = &list
}

func catalogProjectionObject(raw []byte, consume func(string, json.RawMessage) error) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return ErrProtocol
	}
	count := 0
	for d.More() {
		count++
		if count > entryLimit {
			return ErrBounds
		}
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !validKey(key, false) {
			return ErrProtocol
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return ErrProtocol
		}
		if err := consume(key, value); err != nil {
			return err
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return ErrProtocol
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrProtocol
	}
	return nil
}

func decodeCatalogProjection(raw []byte, target catalogTarget) (contract.KVHTTPCatalogList, error) {
	if !slices.Contains([]string{"datacenters", "nodes", "services", "service", "node"}, target.kind) || (target.kind == "node" || target.kind == "service") && !validKey(target.name, false) {
		return contract.KVHTTPCatalogList{}, ErrUnsupported
	}
	var valid sessionProjectionJSON
	if err := jsondocument.Decode(raw, &valid, responseLimit); err != nil {
		return contract.KVHTTPCatalogList{}, errors.Join(ErrProtocol, err)
	}
	list := contract.KVHTTPCatalogList{Kind: target.kind}
	budget := &healthProjectionBudget{bytes: 128 + int64(len(target.kind))}
	var err error
	switch target.kind {
	case "datacenters":
		list.Datacenters = []string{}
		seen := map[string]bool{}
		err = sessionProjectionArray(raw, entryLimit, func(value json.RawMessage) error {
			if err := budget.row(); err != nil {
				return err
			}
			var name string
			if json.Unmarshal(value, &name) != nil || !validKey(name, false) || seen[name] {
				return ErrProtocol
			}
			seen[name] = true
			if err := budget.add(24 + int64(len(name))); err != nil {
				return err
			}
			list.Datacenters = append(list.Datacenters, name)
			return nil
		})
	case "nodes":
		list.Nodes = []contract.KVHTTPCatalogNode{}
		seen := map[catalogIdentity]bool{}
		err = sessionProjectionArray(raw, entryLimit, func(value json.RawMessage) error {
			if err := budget.row(); err != nil {
				return err
			}
			node, err := decodeCatalogNode(value)
			if err != nil {
				return err
			}
			id := catalogQualifiedIdentity(node.Node, "", "", node.Partition, node.PeerName, node.PresentFields, node.NullFields)
			if seen[id] {
				return ErrProtocol
			}
			seen[id] = true
			if err := budget.add(catalogNodeCost(node)); err != nil {
				return err
			}
			list.Nodes = append(list.Nodes, node)
			return nil
		})
	case "services":
		list.Services = []contract.KVHTTPCatalogServiceTags{}
		err = catalogProjectionObject(raw, func(name string, value json.RawMessage) error {
			if err := budget.row(); err != nil {
				return err
			}
			entry := contract.KVHTTPCatalogServiceTags{Name: name, Raw: bytes.Clone(value), TagsNull: bytes.Equal(bytes.TrimSpace(value), []byte("null"))}
			if !entry.TagsNull {
				var err error
				entry.Tags, err = healthStringList(value)
				if err != nil {
					return err
				}
			}
			if err := budget.add(catalogTagsCost(entry)); err != nil {
				return err
			}
			list.Services = append(list.Services, entry)
			return nil
		})
	case "service":
		list.Instances = []contract.KVHTTPCatalogInstance{}
		seen := map[catalogIdentity]bool{}
		err = sessionProjectionArray(raw, entryLimit, func(value json.RawMessage) error {
			if err := budget.row(); err != nil {
				return err
			}
			instance, err := decodeCatalogInstance(value)
			if err != nil {
				return err
			}
			if instance.ServiceName != target.name {
				return ErrProtocol
			}
			id := catalogQualifiedIdentity(instance.Node, instance.ServiceID, instance.Namespace, instance.Partition, instance.PeerName, instance.PresentFields, instance.NullFields)
			if seen[id] {
				return ErrProtocol
			}
			seen[id] = true
			if err := budget.add(catalogInstanceCost(instance)); err != nil {
				return err
			}
			list.Instances = append(list.Instances, instance)
			return nil
		})
	case "node":
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			list.Null = true
			break
		}
		fields, present, nulls, decodeErr := sessionProjectionFields(raw)
		if decodeErr != nil {
			return contract.KVHTTPCatalogList{}, decodeErr
		}
		n := contract.KVHTTPCatalogNodeServices{PresentFields: present, NullFields: nulls, Raw: bytes.Clone(raw)}
		n.Node, err = decodeCatalogNode(fields["Node"])
		if err != nil {
			return contract.KVHTTPCatalogList{}, err
		}
		if n.Node.Node != target.name {
			return contract.KVHTTPCatalogList{}, ErrProtocol
		}
		if err = budget.row(); err != nil {
			return contract.KVHTTPCatalogList{}, err
		}
		if value, ok := fields["Services"]; ok && !slices.Contains(nulls, "Services") {
			n.Services = []contract.KVHTTPCatalogNodeService{}
			err = catalogProjectionObject(value, func(key string, value json.RawMessage) error {
				if err := budget.row(); err != nil {
					return err
				}
				service, err := decodeHealthService(value)
				if err != nil {
					return err
				}
				if service.ID != key {
					return ErrProtocol
				}
				n.Services = append(n.Services, contract.KVHTTPCatalogNodeService{Key: key, Service: service, Raw: bytes.Clone(value)})
				return budget.add(catalogNodeServiceCost(n.Services[len(n.Services)-1]))
			})
		}
		if err == nil {
			without := n
			without.Services = nil
			err = budget.add(catalogNodeServicesCost(without))
		}
		list.NodeServices = &n
	}
	if err != nil {
		return contract.KVHTTPCatalogList{}, err
	}
	return list, nil
}

type catalogIdentity struct {
	node, id, namespace, partition, peer                             string
	namespacePresent, namespaceNull, partitionPresent, partitionNull bool
	peerPresent, peerNull                                            bool
}

func catalogQualifiedIdentity(node, id, namespace, partition, peer string, present, nulls []string) catalogIdentity {
	return catalogIdentity{node, id, namespace, partition, peer, slices.Contains(present, "Namespace"), slices.Contains(nulls, "Namespace"), slices.Contains(present, "Partition"), slices.Contains(nulls, "Partition"), slices.Contains(present, "PeerName"), slices.Contains(nulls, "PeerName")}
}

func decodeCatalogNode(raw json.RawMessage) (contract.KVHTTPCatalogNode, error) {
	fields, present, nulls, err := sessionProjectionFields(raw)
	n := contract.KVHTTPCatalogNode{PresentFields: present, NullFields: nulls, Raw: bytes.Clone(raw)}
	if err != nil {
		return n, err
	}
	if err = healthStrings(fields, nulls, map[string]*string{"ID": &n.ID, "Node": &n.Node, "Address": &n.Address, "Datacenter": &n.Datacenter, "Partition": &n.Partition, "PeerName": &n.PeerName}); err != nil {
		return n, err
	}
	if err = healthOptionalStringAlias(fields, "ID", "Id", &n.ID); err != nil || !validKey(n.Node, false) {
		return n, ErrProtocol
	}
	if err = healthUints(fields, nulls, map[string]*uint64{"CreateIndex": &n.CreateIndex, "ModifyIndex": &n.ModifyIndex}); err != nil {
		return n, err
	}
	for key, ptr := range map[string]*map[string]string{"TaggedAddresses": &n.TaggedAddresses, "Meta": &n.Meta} {
		if value, ok := fields[key]; ok && !slices.Contains(nulls, key) {
			*ptr, err = healthStringMap(value)
			if err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func decodeCatalogInstance(raw json.RawMessage) (contract.KVHTTPCatalogInstance, error) {
	fields, present, nulls, err := sessionProjectionFields(raw)
	i := contract.KVHTTPCatalogInstance{PresentFields: present, NullFields: nulls, Raw: bytes.Clone(raw)}
	if err != nil {
		return i, err
	}
	if err = healthStrings(fields, nulls, map[string]*string{"ID": &i.ID, "Node": &i.Node, "Address": &i.Address, "Datacenter": &i.Datacenter, "Namespace": &i.Namespace, "Partition": &i.Partition, "PeerName": &i.PeerName, "ServiceID": &i.ServiceID, "ServiceName": &i.ServiceName, "ServiceAddress": &i.ServiceAddress, "ServiceKind": &i.ServiceKind}); err != nil {
		return i, err
	}
	if err = healthOptionalStringAlias(fields, "ID", "Id", &i.ID); err != nil {
		return i, err
	}
	if err = healthOptionalStringAlias(fields, "ServiceID", "ServiceId", &i.ServiceID); err != nil || !validKey(i.Node, false) || !validKey(i.ServiceID, false) || !validKey(i.ServiceName, false) {
		return i, ErrProtocol
	}
	if err = healthUints(fields, nulls, map[string]*uint64{"ServicePort": &i.ServicePort, "CreateIndex": &i.CreateIndex, "ModifyIndex": &i.ModifyIndex}); err != nil || i.ServicePort > 65535 {
		return i, ErrProtocol
	}
	for key, ptr := range map[string]*map[string]string{"TaggedAddresses": &i.TaggedAddresses, "NodeMeta": &i.NodeMeta, "ServiceMeta": &i.ServiceMeta} {
		if value, ok := fields[key]; ok && !slices.Contains(nulls, key) {
			*ptr, err = healthStringMap(value)
			if err != nil {
				return i, err
			}
		}
	}
	if value, ok := fields["ServiceTags"]; ok && !slices.Contains(nulls, "ServiceTags") {
		i.ServiceTags, err = healthStringList(value)
		if err != nil {
			return i, err
		}
	}
	if value, ok := fields["ServiceEnableTagOverride"]; ok && !slices.Contains(nulls, "ServiceEnableTagOverride") {
		if json.Unmarshal(value, &i.ServiceEnableTagOverride) != nil {
			return i, ErrProtocol
		}
	}
	if value, ok := fields["ServiceTaggedAddresses"]; ok && !slices.Contains(nulls, "ServiceTaggedAddresses") {
		addresses, _, _, err := sessionProjectionFields(value)
		if err != nil {
			return i, err
		}
		i.ServiceTaggedAddresses = make(map[string]contract.KVHTTPHealthServiceAddress, len(addresses))
		for key, value := range addresses {
			fields, p, n, err := sessionProjectionFields(value)
			if err != nil {
				return i, err
			}
			a := contract.KVHTTPHealthServiceAddress{PresentFields: p, NullFields: n}
			if err = healthStrings(fields, n, map[string]*string{"Address": &a.Address}); err != nil {
				return i, err
			}
			if err = healthUints(fields, n, map[string]*uint64{"Port": &a.Port}); err != nil || a.Port > 65535 {
				return i, ErrProtocol
			}
			i.ServiceTaggedAddresses[key] = a
		}
	}
	if value, ok := fields["ServiceWeights"]; ok && !slices.Contains(nulls, "ServiceWeights") {
		fields, p, n, err := sessionProjectionFields(value)
		if err != nil {
			return i, err
		}
		i.ServiceWeights.PresentFields, i.ServiceWeights.NullFields = p, n
		if err = healthUints(fields, n, map[string]*uint64{"Passing": &i.ServiceWeights.Passing, "Warning": &i.ServiceWeights.Warning}); err != nil {
			return i, err
		}
	}
	return i, nil
}

func catalogNodeCost(n contract.KVHTTPCatalogNode) int64 {
	return int64(384+len(n.Raw)) + healthFieldsCost(n.PresentFields, n.NullFields) + healthStringsCost(n.ID, n.Node, n.Address, n.Datacenter, n.Partition, n.PeerName) + healthMapCost(n.TaggedAddresses) + healthMapCost(n.Meta)
}

func catalogTagsCost(s contract.KVHTTPCatalogServiceTags) int64 {
	return int64(96+len(s.Name)+len(s.Raw)) + healthFieldsCost(s.Tags)
}

func catalogInstanceCost(i contract.KVHTTPCatalogInstance) int64 {
	cost := int64(768+len(i.Raw)) + healthFieldsCost(i.PresentFields, i.NullFields, i.ServiceTags, i.ServiceWeights.PresentFields, i.ServiceWeights.NullFields) + healthStringsCost(i.ID, i.Node, i.Address, i.Datacenter, i.Namespace, i.Partition, i.PeerName, i.ServiceID, i.ServiceName, i.ServiceAddress, i.ServiceKind) + healthMapCost(i.TaggedAddresses) + healthMapCost(i.NodeMeta) + healthMapCost(i.ServiceMeta)
	for key, a := range i.ServiceTaggedAddresses {
		cost += 128 + int64(len(key)+len(a.Address)) + healthFieldsCost(a.PresentFields, a.NullFields)
	}
	return cost
}

func catalogNodeServiceCost(s contract.KVHTTPCatalogNodeService) int64 {
	return int64(96+len(s.Key)+len(s.Raw)) + healthInstanceCost(contract.KVHTTPHealthInstance{Service: s.Service})
}

func catalogNodeServicesCost(n contract.KVHTTPCatalogNodeServices) int64 {
	cost := int64(128+len(n.Raw)) + healthFieldsCost(n.PresentFields, n.NullFields) + catalogNodeCost(n.Node)
	for _, s := range n.Services {
		cost += catalogNodeServiceCost(s)
	}
	return cost
}

func catalogProjectionCost(l contract.KVHTTPCatalogList) int64 {
	cost := int64(128+len(l.Kind)) + healthFieldsCost(l.Datacenters)
	for _, n := range l.Nodes {
		cost += catalogNodeCost(n)
	}
	for _, s := range l.Services {
		cost += catalogTagsCost(s)
	}
	for _, i := range l.Instances {
		cost += catalogInstanceCost(i)
	}
	if l.NodeServices != nil {
		cost += catalogNodeServicesCost(*l.NodeServices)
	}
	return cost
}
