package contract

import (
	"maps"
	"slices"
)

// Catalog projections describe one original receipt and its ACL boundary.
// Raw retains the complete JSON value, including fields outside the projection.
// These identities do not establish Agent incarnation or write permission.
type KVHTTPCatalogNode struct {
	PresentFields, NullFields                          []string
	ID, Node, Address, Datacenter, Partition, PeerName string
	TaggedAddresses, Meta                              map[string]string
	CreateIndex, ModifyIndex                           uint64
	Raw                                                []byte
}

func (n KVHTTPCatalogNode) Clone() KVHTTPCatalogNode {
	n.PresentFields, n.NullFields = slices.Clone(n.PresentFields), slices.Clone(n.NullFields)
	n.TaggedAddresses, n.Meta, n.Raw = maps.Clone(n.TaggedAddresses), maps.Clone(n.Meta), slices.Clone(n.Raw)
	return n
}

type KVHTTPCatalogServiceTags struct {
	Name     string
	Tags     []string
	TagsNull bool
	Raw      []byte
}

func (s KVHTTPCatalogServiceTags) Clone() KVHTTPCatalogServiceTags {
	s.Tags, s.Raw = slices.Clone(s.Tags), slices.Clone(s.Raw)
	return s
}

// Catalog service instances are flat ServiceNode objects, unlike nested health
// instances. Indices remain uint64; tag order remains the captured native order.
type KVHTTPCatalogInstance struct {
	PresentFields, NullFields                                     []string
	ID, Node, Address, Datacenter, Namespace, Partition, PeerName string
	ServiceID, ServiceName, ServiceAddress, ServiceKind           string
	TaggedAddresses, NodeMeta, ServiceMeta                        map[string]string
	ServiceTags                                                   []string
	ServiceTaggedAddresses                                        map[string]KVHTTPHealthServiceAddress
	ServiceWeights                                                KVHTTPHealthServiceWeights
	ServicePort, CreateIndex, ModifyIndex                         uint64
	ServiceEnableTagOverride                                      bool
	Raw                                                           []byte
}

func (i KVHTTPCatalogInstance) Clone() KVHTTPCatalogInstance {
	i.PresentFields, i.NullFields = slices.Clone(i.PresentFields), slices.Clone(i.NullFields)
	i.TaggedAddresses, i.NodeMeta, i.ServiceMeta = maps.Clone(i.TaggedAddresses), maps.Clone(i.NodeMeta), maps.Clone(i.ServiceMeta)
	i.ServiceTags, i.Raw = slices.Clone(i.ServiceTags), slices.Clone(i.Raw)
	i.ServiceTaggedAddresses = maps.Clone(i.ServiceTaggedAddresses)
	for k, v := range i.ServiceTaggedAddresses {
		i.ServiceTaggedAddresses[k] = v.Clone()
	}
	i.ServiceWeights = i.ServiceWeights.Clone()
	return i
}

type KVHTTPCatalogNodeService struct {
	Key     string
	Service KVHTTPHealthService
	Raw     []byte
}

func (s KVHTTPCatalogNodeService) Clone() KVHTTPCatalogNodeService {
	s.Service, s.Raw = s.Service.Clone(), slices.Clone(s.Raw)
	return s
}

type KVHTTPCatalogNodeServices struct {
	PresentFields, NullFields []string
	Node                      KVHTTPCatalogNode
	Services                  []KVHTTPCatalogNodeService
	Raw                       []byte
}

func (n KVHTTPCatalogNodeServices) Clone() KVHTTPCatalogNodeServices {
	n.PresentFields, n.NullFields = slices.Clone(n.PresentFields), slices.Clone(n.NullFields)
	n.Node, n.Raw = n.Node.Clone(), slices.Clone(n.Raw)
	n.Services = slices.Clone(n.Services)
	for i := range n.Services {
		n.Services[i] = n.Services[i].Clone()
	}
	return n
}

// Null records the native null response for one node lookup. It is distinct
// from an empty service map and from an ACL-filtered incomplete receipt.
type KVHTTPCatalogList struct {
	Kind                         string
	Datacenters                  []string
	Nodes                        []KVHTTPCatalogNode
	Services                     []KVHTTPCatalogServiceTags
	Instances                    []KVHTTPCatalogInstance
	NodeServices                 *KVHTTPCatalogNodeServices
	Null                         bool
	ProjectionComplete, Complete bool
}

func (l KVHTTPCatalogList) Clone() KVHTTPCatalogList {
	l.Datacenters = slices.Clone(l.Datacenters)
	l.Nodes = slices.Clone(l.Nodes)
	for i := range l.Nodes {
		l.Nodes[i] = l.Nodes[i].Clone()
	}
	l.Services = slices.Clone(l.Services)
	for i := range l.Services {
		l.Services[i] = l.Services[i].Clone()
	}
	l.Instances = slices.Clone(l.Instances)
	for i := range l.Instances {
		l.Instances[i] = l.Instances[i].Clone()
	}
	if l.NodeServices != nil {
		n := l.NodeServices.Clone()
		l.NodeServices = &n
	}
	return l
}
