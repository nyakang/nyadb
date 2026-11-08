package meilisearch

import "github.com/nyakang/nyadb/internal/db/contract"

type namespaceScope struct {
	version uint64
	owner   string
}

func (s *Session) namespaceVersion(uid string) (uint64, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.namespaceScopes[uid]
	return scope.version, scope.owner
}
func (s *Session) bindNamespaceLease(l *queryLease, uid, allowedOwner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return contract.ErrRevoked
	}
	if uid != "" {
		if owner := s.namespaceScopes[uid].owner; owner != "" && owner != allowedOwner {
			return contract.ErrNamespaceBusy
		}
	}
	l.namespace = uid
	return nil
}
func namespaceMutationRefs(n *contract.NamespaceMutation) []contract.NamespaceRef {
	if n == nil {
		return nil
	}
	refs := []contract.NamespaceRef{n.Draft.Ref}
	if n.Draft.Destination.Catalog != "" {
		refs = append(refs, n.Draft.Destination)
	}
	return refs
}
func (s *Session) claimNamespace(owner string, n *contract.NamespaceMutation, actor *queryLease, previousOwner string, recovery bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return contract.ErrRevoked
	}
	refs := namespaceMutationRefs(n)
	for i, ref := range refs {
		scope := s.namespaceScopes[ref.Catalog]
		if scope.owner != "" && scope.owner != previousOwner {
			return contract.ErrNamespaceBusy
		}
		if !recovery && (i >= len(n.Versions) || scope.version != n.Versions[i]) {
			return contract.ErrNamespaceChanged
		}
		for l := range s.documents {
			if l.object.Catalog == ref.Catalog {
				return contract.ErrNamespaceInUse
			}
		}
		for l := range s.leases {
			if l != actor && l.namespace == ref.Catalog {
				return contract.ErrNamespaceInUse
			}
		}
	}
	if s.namespaceScopes == nil {
		s.namespaceScopes = map[string]namespaceScope{}
	}
	for _, ref := range refs {
		scope := s.namespaceScopes[ref.Catalog]
		scope.version++
		scope.owner = owner
		s.namespaceScopes[ref.Catalog] = scope
	}
	return nil
}
func (s *Session) releaseNamespace(owner string) {
	if owner == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for uid, scope := range s.namespaceScopes {
		if scope.owner == owner {
			scope.owner = ""
			scope.version++
			s.namespaceScopes[uid] = scope
		}
	}
}
