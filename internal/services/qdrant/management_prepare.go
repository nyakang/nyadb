package qdrant

import (
	"encoding/json"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func managementAliasMap(o ManagementObservation) map[string]string {
	out := map[string]string{}
	for _, a := range o.Aliases {
		out[a.Alias] = a.Collection
	}
	return out
}
func simulateAliasChanges(o ManagementObservation, changes []AliasChange) (map[string]string, []string, error) {
	aliases := managementAliasMap(o)
	claims := []string{}
	for _, a := range changes {
		switch a.Action {
		case "create_alias":
			if _, exists := aliases[a.Alias]; exists || slices.Contains(o.Collections, a.Alias) {
				return nil, nil, contract.ErrNamespaceExists
			}
			if !slices.Contains(o.Collections, a.Collection) {
				return nil, nil, contract.ErrNamespaceChanged
			}
			aliases[a.Alias] = a.Collection
			claims = append(claims, a.Collection)
		case "delete_alias", "rename_alias":
			collection, exists := aliases[a.Alias]
			if !exists || !slices.Contains(o.Collections, collection) {
				return nil, nil, contract.ErrNamespaceChanged
			}
			claims = append(claims, collection)
			delete(aliases, a.Alias)
			if a.Action == "rename_alias" {
				if _, exists := aliases[a.Destination]; exists || slices.Contains(o.Collections, a.Destination) {
					return nil, nil, contract.ErrNamespaceExists
				}
				aliases[a.Destination] = collection
			}
		default:
			return nil, nil, ErrInvalid
		}
	}
	slices.Sort(claims)
	claims = slices.Compact(claims)
	for _, name := range claims {
		if !slices.Contains(o.Targets, name) {
			return nil, nil, contract.ErrNamespaceChanged
		}
	}
	return aliases, claims, nil
}

func (l *ManagementLease) prepareReady(o ManagementObservation) error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || !l.session.Alive() {
		return contract.ErrRevoked
	}
	return l.validateBefore(o)
}
func (l *ManagementLease) PrepareCollection(before ManagementObservation, collection, action string, config json.RawMessage, timeoutSeconds int) (ManagedPlan, error) {
	if !l.operation.TryLock() {
		return ManagedPlan{}, ErrBusy
	}
	defer l.operation.Unlock()
	native, err := PrepareCollectionManagement(collection, action, config, timeoutSeconds)
	if err != nil {
		return ManagedPlan{}, err
	}
	if err := l.prepareReady(before); err != nil {
		return ManagedPlan{}, err
	}
	if !slices.Contains(before.Targets, collection) {
		return ManagedPlan{}, contract.ErrNamespaceChanged
	}
	exists := slices.Contains(before.Collections, collection)
	if action == "collection_create" {
		if exists || managementAliasMap(before)[collection] != "" {
			return ManagedPlan{}, contract.ErrNamespaceExists
		}
	} else if !exists || len(before.Configs[collection]) == 0 {
		return ManagedPlan{}, contract.ErrNamespaceChanged
	}
	l.planSequence++
	return ManagedPlan{lease: l, sequence: l.planSequence, before: cloneManagementObservation(before), native: native, claims: []string{collection}}, nil
}
func (l *ManagementLease) PrepareAliases(before ManagementObservation, changes []AliasChange, timeoutSeconds int) (ManagedPlan, error) {
	if !l.operation.TryLock() {
		return ManagedPlan{}, ErrBusy
	}
	defer l.operation.Unlock()
	if len(changes) == 0 || len(changes) > 1000 {
		return ManagedPlan{}, ErrBounds
	}
	changes = slices.Clone(changes)
	native, err := PrepareAliasManagement(changes, timeoutSeconds)
	if err != nil {
		return ManagedPlan{}, err
	}
	if err := l.prepareReady(before); err != nil {
		return ManagedPlan{}, err
	}
	_, claims, err := simulateAliasChanges(before, changes)
	if err != nil {
		return ManagedPlan{}, err
	}
	l.planSequence++
	return ManagedPlan{lease: l, sequence: l.planSequence, before: cloneManagementObservation(before), native: native, claims: claims, aliases: slices.Clone(changes)}, nil
}

// A claim covers only the captured local session. Remote changes can still race
// live preflight; no native management endpoint supplies CAS.
func (l *ManagementLease) claim(p ManagedPlan) error {
	s := l.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return contract.ErrRevoked
	}
	if s.namespaceOwner != nil && s.namespaceOwner != l {
		return contract.ErrNamespaceBusy
	}
	// Arbitrary REST documents can touch any collection or alias.
	if len(s.leases) != 0 {
		return ErrManagementInUse
	}
	for d := range s.documents {
		if slices.Contains(p.claims, d.object.Catalog) {
			return ErrManagementInUse
		}
	}
	s.namespaceOwner = l
	s.claimedCollections = map[string]bool{}
	for _, name := range p.claims {
		s.claimedCollections[name] = true
	}
	return nil
}
func (l *ManagementLease) releaseClaim() {
	s := l.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.namespaceOwner == l {
		s.namespaceOwner = nil
		s.claimedCollections = nil
	}
}

func managedAliasNames(p ManagedPlan, o ManagementObservation) []string {
	names := []string{}
	if p.native.action == "aliases" {
		for _, a := range p.aliases {
			names = append(names, a.Alias)
			if a.Destination != "" {
				names = append(names, a.Destination)
			}
		}
	} else {
		names = append(names, p.native.collection)
		if p.native.action == "collection_delete" {
			for _, a := range o.Aliases {
				if a.Collection == p.native.collection {
					names = append(names, a.Alias)
				}
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
func managementPreflightMatches(p ManagedPlan, actual ManagementObservation) bool {
	for _, name := range p.claims {
		if slices.Contains(p.before.Collections, name) != slices.Contains(actual.Collections, name) {
			return false
		}
		before, after := p.before.Configs[name], actual.Configs[name]
		if len(before) != len(after) || string(before) != string(after) {
			return false
		}
	}
	names := append(managedAliasNames(p, p.before), managedAliasNames(p, actual)...)
	a, b := managementAliasMap(p.before), managementAliasMap(actual)
	for _, name := range names {
		if a[name] != b[name] || slices.Contains(p.before.Collections, name) != slices.Contains(actual.Collections, name) {
			return false
		}
	}
	return true
}
