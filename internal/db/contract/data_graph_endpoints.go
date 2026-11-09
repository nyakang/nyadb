package contract

// DataGraphEndpoints is a complete native observation of the directed endpoints
// for a new relationship in one original data document. Token belongs to that
// document; element IDs and node content do not establish database incarnation.
type DataGraphEndpoints struct {
	Object         ObjectRef
	Source, Target GraphNode
	NativeJSON     string
	Token          string
}

func (e DataGraphEndpoints) Clone() DataGraphEndpoints {
	e.Source, e.Target = e.Source.Clone(), e.Target.Clone()
	return e
}

func CloneDataGraphEndpoints(e *DataGraphEndpoints) *DataGraphEndpoints {
	if e == nil {
		return nil
	}
	copy := e.Clone()
	return &copy
}
