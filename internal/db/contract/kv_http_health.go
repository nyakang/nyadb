package contract

import (
	"maps"
	"slices"
)

// Duration retains the native representation. Older Consul definitions may
// return integer nanoseconds; other versions return a duration string.
type KVHTTPHealthDuration struct {
	Text        string
	Nanoseconds *uint64
}

func (d KVHTTPHealthDuration) Clone() KVHTTPHealthDuration {
	d.Nanoseconds = clonePointer(d.Nanoseconds)
	return d
}

type KVHTTPHealthDefinition struct {
	PresentFields, NullFields []string
	HTTP, TCP, GRPC           string
	Interval, Timeout, TTL    KVHTTPHealthDuration
}

func (d KVHTTPHealthDefinition) Clone() KVHTTPHealthDefinition {
	d.PresentFields, d.NullFields = slices.Clone(d.PresentFields), slices.Clone(d.NullFields)
	d.Interval, d.Timeout, d.TTL = d.Interval.Clone(), d.Timeout.Clone(), d.TTL.Clone()
	return d
}

type KVHTTPHealthCheck struct {
	PresentFields, NullFields                                                                      []string
	Node, CheckID, Name, Status, Notes, Output, ServiceID, ServiceName, Type, Namespace, Partition string
	ServiceTags                                                                                    []string
	ExposedPort, CreateIndex, ModifyIndex                                                          uint64
	Definition                                                                                     KVHTTPHealthDefinition
	Interval, Timeout                                                                              KVHTTPHealthDuration
}

func (c KVHTTPHealthCheck) Clone() KVHTTPHealthCheck {
	c.PresentFields, c.NullFields = slices.Clone(c.PresentFields), slices.Clone(c.NullFields)
	c.ServiceTags = slices.Clone(c.ServiceTags)
	c.Definition = c.Definition.Clone()
	c.Interval, c.Timeout = c.Interval.Clone(), c.Timeout.Clone()
	return c
}

// Maintenance is derived locally from the original CheckID. It does not
// replace the native Status or claim permission to change a check.
func (c KVHTTPHealthCheck) Maintenance() bool {
	return c.CheckID == "_node_maintenance" || len(c.CheckID) >= len("_service_maintenance:") && c.CheckID[:len("_service_maintenance:")] == "_service_maintenance:"
}

type KVHTTPHealthNode struct {
	NodeMeta                                            map[string]string
	PresentFields, NullFields                           []string
	ID, Node, Address, Datacenter, Namespace, Partition string
	TaggedAddresses, Meta                               map[string]string
	CreateIndex, ModifyIndex                            uint64
}

func (n KVHTTPHealthNode) Clone() KVHTTPHealthNode {
	n.PresentFields, n.NullFields = slices.Clone(n.PresentFields), slices.Clone(n.NullFields)
	n.TaggedAddresses, n.Meta, n.NodeMeta = maps.Clone(n.TaggedAddresses), maps.Clone(n.Meta), maps.Clone(n.NodeMeta)
	return n
}

type KVHTTPHealthServiceAddress struct {
	PresentFields, NullFields []string
	Address                   string
	Port                      uint64
}

func (a KVHTTPHealthServiceAddress) Clone() KVHTTPHealthServiceAddress {
	a.PresentFields, a.NullFields = slices.Clone(a.PresentFields), slices.Clone(a.NullFields)
	return a
}

type KVHTTPHealthServiceWeights struct {
	PresentFields, NullFields []string
	Passing, Warning          uint64
}

func (w KVHTTPHealthServiceWeights) Clone() KVHTTPHealthServiceWeights {
	w.PresentFields, w.NullFields = slices.Clone(w.PresentFields), slices.Clone(w.NullFields)
	return w
}

type KVHTTPHealthService struct {
	PresentFields, NullFields                        []string
	ID, Service, Address, Kind, Namespace, Partition string
	Tags                                             []string
	Meta                                             map[string]string
	TaggedAddresses                                  map[string]KVHTTPHealthServiceAddress
	Port, CreateIndex, ModifyIndex                   uint64
	EnableTagOverride                                bool
	Weights                                          KVHTTPHealthServiceWeights
}

func (s KVHTTPHealthService) Clone() KVHTTPHealthService {
	s.PresentFields, s.NullFields = slices.Clone(s.PresentFields), slices.Clone(s.NullFields)
	s.Tags, s.Meta = slices.Clone(s.Tags), maps.Clone(s.Meta)
	s.TaggedAddresses = maps.Clone(s.TaggedAddresses)
	for k, v := range s.TaggedAddresses {
		s.TaggedAddresses[k] = v.Clone()
	}
	s.Weights = s.Weights.Clone()
	return s
}

type KVHTTPHealthInstance struct {
	PresentFields, NullFields []string
	Node                      KVHTTPHealthNode
	Service                   KVHTTPHealthService
	Checks                    []KVHTTPHealthCheck
}

func (i KVHTTPHealthInstance) Clone() KVHTTPHealthInstance {
	i.PresentFields, i.NullFields = slices.Clone(i.PresentFields), slices.Clone(i.NullFields)
	i.Node, i.Service = i.Node.Clone(), i.Service.Clone()
	i.Checks = slices.Clone(i.Checks)
	for k := range i.Checks {
		i.Checks[k] = i.Checks[k].Clone()
	}
	return i
}

// These lists index one captured response. Complete describes its scoped
// projection and ACL boundary, never an atomic inventory, Agent incarnation
// or write authority. Body retains all unknown original JSON fields.
type KVHTTPHealthList struct {
	Kind                         string
	Checks                       []KVHTTPHealthCheck
	Instances                    []KVHTTPHealthInstance
	ProjectionComplete, Complete bool
}

func (l KVHTTPHealthList) Clone() KVHTTPHealthList {
	l.Checks = slices.Clone(l.Checks)
	for i := range l.Checks {
		l.Checks[i] = l.Checks[i].Clone()
	}
	l.Instances = slices.Clone(l.Instances)
	for i := range l.Instances {
		l.Instances[i] = l.Instances[i].Clone()
	}
	return l
}
