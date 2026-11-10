package contract

import "slices"

// Type is the original native enum number. Unknown reply values remain
// inspectable; their presence does not authorize an unsupported write.
type KVAlarm struct {
	MemberID         uint64
	Type             int32
	NativeExtensions []byte
}

func (a KVAlarm) Clone() KVAlarm {
	a.NativeExtensions = slices.Clone(a.NativeExtensions)
	return a
}
