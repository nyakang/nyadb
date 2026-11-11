package contract

// DataKeyCopy captures an original complete row observation and a distinct,
// qualified destination key. It is accepted only by a bound key-copy preview;
// ordinary insert/update savers must not infer conditional copy semantics.
type DataKeyCopy struct {
	Source      *RowSnapshot
	Destination string
}

func CloneDataKeyCopy(r DataKeyCopy) DataKeyCopy {
	if r.Source != nil {
		r.Source = CloneDataMutationSnapshot(DataMutationSnapshot{Edits: []DataEdit{{Original: r.Source}}}).Edits[0].Original
	}
	return r
}
