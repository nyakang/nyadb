package objectworkspace

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type objectReferenceRequest struct {
	profile    int64
	generation uint64
	active     Session
	cancel     context.CancelFunc
	results    <-chan objectSearchResult
}

func (s *State) closeObjectReference() {
	if x := s.referenceRequest; x != nil {
		x.cancel()
	}
	s.referenceRequest = nil
}

// ResolveObjectReference obtains a fresh full metadata snapshot for a public
// favorite reference. The request neither executes SQL nor writes session caches.
func (s *State) ResolveObjectReference(services ui.Services, profile int64, generation uint64, ref contract.ObjectRef, label string) {
	if !s.CanSearchObjects(profile) || s.sessionFor(profile).Generation != generation || ref.ProfileID != profile || label == "" {
		return
	}
	runtime, err := s.deps.SearchRuntime(profile)
	if err != nil {
		return
	}
	s.closeObjectReference()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	out := make(chan objectSearchResult, 1)
	s.referenceRequest = &objectReferenceRequest{profile: profile, generation: generation, active: s.session(), cancel: cancel, results: out}
	go func() {
		defer cancel()
		objects, err := runtime.Browse(ctx, ref.Namespace())
		result := objectSearchResult{err: err}
		if err == nil {
			for _, object := range objects {
				if object.Ref == (contract.ObjectRef{}) {
					object.Ref = ref.Namespace().Object(searchObjectKind(object.Kind), object.Name)
				}
				if object.Ref == ref && object.Name == ref.Name && searchObjectKind(object.Kind) == ref.Kind {
					result.entries = []ObjectSearchEntry{{Object: object, Namespace: contract.Namespace{Ref: ref.Namespace(), Label: label}}}
					break
				}
			}
			if len(result.entries) == 0 {
				result.err = errors.New("收藏对象已删除或原身份已变化，请刷新收藏")
			}
		}
		out <- result
		services.Invalidate()
	}()
}
func (s *State) consumeObjectReference(services ui.Services) {
	x := s.referenceRequest
	if x == nil {
		return
	}
	if w := s.sessionFor(x.profile); !w.Connected || w.Generation != x.generation || s.session() != x.active {
		s.closeObjectReference()
		return
	}
	select {
	case r := <-x.results:
		s.closeObjectReference()
		if r.err != nil {
			s.report(r.err.Error())
			return
		}
		if len(r.entries) == 1 && s.deps.OpenSearchObject != nil {
			s.deps.OpenSearchObject(services, x.profile, x.generation, r.entries[0])
		}
	default:
	}
}
