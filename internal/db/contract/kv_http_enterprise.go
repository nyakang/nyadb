package contract

import (
	"context"
	"slices"
)

type KVHTTPEnterpriseImpactObservation struct {
	Resource, Namespace, Partition string
	Count                          int
	HTTP                           KVResult
	Complete                       bool
}

func (o KVHTTPEnterpriseImpactObservation) Clone() KVHTTPEnterpriseImpactObservation {
	o.HTTP = o.HTTP.Clone()
	return o
}

type KVHTTPEnterpriseImpact struct {
	Scope                 KVScope
	Request               KVHTTPManagementRequest
	Observations          []KVHTTPEnterpriseImpactObservation
	Missing               []string
	Complete, ACLFiltered bool
}

func (i KVHTTPEnterpriseImpact) Clone() KVHTTPEnterpriseImpact {
	i.Scope = i.Scope.Clone()
	i.Request = i.Request.Clone()
	i.Observations = slices.Clone(i.Observations)
	for n := range i.Observations {
		i.Observations[n] = i.Observations[n].Clone()
	}
	i.Missing = slices.Clone(i.Missing)
	return i
}

type KVHTTPEnterpriseDeletionPreparation interface {
	KVHTTPManagementPreparation
	Impact() KVHTTPEnterpriseImpact
	Close() error
}
type KVHTTPEnterpriseDeletionPlanning interface {
	PrepareHTTPEnterpriseDeletion(context.Context, KVHTTPManagementRequest) (KVHTTPEnterpriseDeletionPreparation, KVHTTPEnterpriseImpact, error)
}
