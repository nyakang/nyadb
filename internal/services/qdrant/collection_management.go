package qdrant

import (
	"bytes"
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func collectionManagementAction(action string) bool {
	switch action {
	case "collection_create", "collection_update", "collection_delete", "aliases":
		return true
	}
	return false
}
func (s *Session) SupportsCollectionManagement(o contract.ObjectRef, action string) bool {
	return s.Alive() && o == (contract.ObjectRef{ProfileID: s.ProfileID(), Kind: "collection_management", Name: "collection_management"}) && collectionManagementAction(action)
}
func (s *Session) ReserveCollectionManagementDocument(o contract.ObjectRef, document uint64) (contract.CollectionManagementLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	if !s.SupportsCollectionManagement(o, "collection_create") {
		return nil, contract.ErrUnsupported
	}
	l, err := s.ReserveManagementDocument(document)
	if err != nil {
		return nil, err
	}
	return &collectionManagementDocument{object: o, native: l}, nil
}

type collectionManagementDocument struct {
	object contract.ObjectRef
	native *ManagementLease
}
type collectionManagementObservation struct {
	owner  *collectionManagementDocument
	native ManagementObservation
}
type collectionManagementPlan struct {
	owner  *collectionManagementDocument
	native ManagedPlan
}

func (d *collectionManagementDocument) Object() contract.ObjectRef { return d.object }
func (d *collectionManagementDocument) Generation() uint64         { return d.native.session.Generation() }
func (d *collectionManagementDocument) name(ref contract.NamespaceRef) (string, error) {
	if ref.ProfileID != d.object.ProfileID || ref.Schema != "" {
		return "", ErrInvalid
	}
	if _, err := collectionPath(ref.Catalog); err != nil {
		return "", err
	}
	return ref.Catalog, nil
}
func (d *collectionManagementDocument) ref(name string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: d.object.ProfileID, Catalog: name}
}
func (d *collectionManagementDocument) snapshot(o ManagementObservation) contract.CollectionManagementSnapshot {
	s := contract.CollectionManagementSnapshot{Object: d.object, Sequence: o.Sequence}
	for _, name := range o.Targets {
		s.Targets = append(s.Targets, d.ref(name))
	}
	for _, name := range o.Collections {
		s.Collections = append(s.Collections, d.ref(name))
	}
	for _, alias := range o.Aliases {
		s.Aliases = append(s.Aliases, contract.CollectionAlias{Alias: d.ref(alias.Alias), Collection: d.ref(alias.Collection)})
	}
	for _, name := range o.Targets {
		if raw, exists := o.Configs[name]; exists {
			s.Configurations = append(s.Configurations, contract.CollectionConfiguration{Collection: d.ref(name), JSON: string(raw)})
		}
	}
	for _, read := range o.Responses {
		s.Reads = append(s.Reads, contract.CollectionManagementRead{Response: collectionManagementResponse(read.Response), BodyTruncated: read.BodyTruncated, OriginalBodyBytes: read.OriginalBodyBytes})
	}
	return s
}
func collectionManagementResponse(r Response) contract.CollectionManagementResponse {
	return contract.CollectionManagementResponse{Status: r.Status, ContentType: r.ContentType, Body: bytes.Clone(r.Body), Incomplete: r.Incomplete}
}
func (o *collectionManagementObservation) Snapshot() contract.CollectionManagementSnapshot {
	return o.owner.snapshot(o.native)
}
func (p *collectionManagementPlan) Action() string { return p.native.Action() }
func (p *collectionManagementPlan) Preview() contract.CollectionManagementRequest {
	r := p.native.Preview()
	return contract.CollectionManagementRequest{Method: r.Method, Path: r.Path, ContentType: r.ContentType, Body: r.Body}
}
func (p *collectionManagementPlan) Affected() []contract.NamespaceRef {
	out := make([]contract.NamespaceRef, len(p.native.claims))
	for i, name := range p.native.claims {
		out[i] = p.owner.ref(name)
	}
	return out
}
func (d *collectionManagementDocument) Read(ctx context.Context, targets []contract.NamespaceRef) (contract.CollectionManagementObservation, error) {
	if len(targets) == 0 || len(targets) > 100 {
		return nil, ErrBounds
	}
	names := make([]string, len(targets))
	for i, ref := range targets {
		name, err := d.name(ref)
		if err != nil {
			return nil, err
		}
		names[i] = name
	}
	o, err := d.native.Read(ctx, names)
	return &collectionManagementObservation{owner: d, native: cloneManagementObservation(o)}, err
}
func (d *collectionManagementDocument) Prepare(before contract.CollectionManagementObservation, draft contract.CollectionManagementDraft) (contract.CollectionManagementPlan, error) {
	o, ok := before.(*collectionManagementObservation)
	if !ok || o == nil || o.owner != d {
		return nil, contract.ErrNamespaceChanged
	}
	if len(draft.Aliases) > 1000 {
		return nil, ErrBounds
	}
	draft = contract.CloneCollectionManagementDraft(draft)
	var native ManagedPlan
	var err error
	if draft.Action == "aliases" {
		if draft.Collection != (contract.NamespaceRef{}) || draft.Configuration != "" {
			return nil, ErrInvalid
		}
		changes := make([]AliasChange, len(draft.Aliases))
		for i, a := range draft.Aliases {
			changes[i].Action = a.Action
			changes[i].Alias, err = d.name(a.Alias)
			if err != nil {
				return nil, err
			}
			if a.Collection != (contract.NamespaceRef{}) {
				changes[i].Collection, err = d.name(a.Collection)
				if err != nil {
					return nil, err
				}
			}
			if a.Destination != (contract.NamespaceRef{}) {
				changes[i].Destination, err = d.name(a.Destination)
				if err != nil {
					return nil, err
				}
			}
		}
		native, err = d.native.PrepareAliases(o.native, changes, draft.TimeoutSeconds)
	} else {
		if !collectionManagementAction(draft.Action) || len(draft.Aliases) != 0 {
			return nil, ErrInvalid
		}
		name, e := d.name(draft.Collection)
		if e != nil {
			return nil, e
		}
		native, err = d.native.PrepareCollection(o.native, name, draft.Action, []byte(draft.Configuration), draft.TimeoutSeconds)
	}
	if err != nil {
		return nil, err
	}
	return &collectionManagementPlan{owner: d, native: native}, nil
}
func (d *collectionManagementDocument) result(r ManagedResult) contract.CollectionManagementResult {
	collection := contract.NamespaceRef{}
	if r.Receipt.Collection != "" {
		collection = d.ref(r.Receipt.Collection)
	}
	out := contract.CollectionManagementResult{State: r.State, Receipt: contract.CollectionManagementReceipt{Action: r.Receipt.Action, Collection: collection, HasResult: r.Receipt.HasResult, Result: r.Receipt.Result, Response: collectionManagementResponse(r.Receipt.Response)}, Snapshot: d.snapshot(r.Observation), Matched: r.Matched, States: map[string]string{}}
	for key, value := range r.States {
		out.States[key] = value
	}
	return out
}
func (d *collectionManagementDocument) Submit(ctx context.Context, plan contract.CollectionManagementPlan) (contract.CollectionManagementResult, error) {
	p, ok := plan.(*collectionManagementPlan)
	if !ok || p == nil || p.owner != d {
		return contract.CollectionManagementResult{State: "not_submitted"}, ErrManagementUsed
	}
	r, err := d.native.Submit(ctx, p.native)
	return d.result(r), err
}
func (d *collectionManagementDocument) Verify(ctx context.Context) (contract.CollectionManagementResult, error) {
	r, err := d.native.Verify(ctx)
	return d.result(r), err
}
func (d *collectionManagementDocument) Close() error { return d.native.Close() }

var _ contract.CollectionManagementProvider = (*Session)(nil)
var _ contract.CollectionManagementLease = (*collectionManagementDocument)(nil)
