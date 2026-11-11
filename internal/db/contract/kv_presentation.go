package contract

import "slices"

// Providers publish immutable document templates independently of UI packages.
// Templates carry no dispatch authority; execution still requires preparation.
type KVRequestMethod struct {
	Name    string
	Request KVRequest
}
type KVRequestPresentation struct {
	Product string
	Default KVRequest
	Methods []KVRequestMethod
	Tools   []KVRequestTool
}

func (p KVRequestPresentation) Clone() KVRequestPresentation {
	p.Methods = slices.Clone(p.Methods)
	p.Tools = slices.Clone(p.Tools)
	return p
}

type KVRequestPresentationProvider interface {
	KVRequestPresentation() KVRequestPresentation
}
