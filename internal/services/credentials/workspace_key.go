package credentials

import (
	"encoding/base64"
	"errors"

	keyring "github.com/zalando/go-keyring"
)

// WorkspaceKey uses a separate namespace; existing database password keys stay compatible.
type WorkspaceKey struct{}

func (WorkspaceKey) Get() ([]byte, bool, error) {
	v, e := keyring.Get("NyaDB SQL workspace", "master-v1")
	if errors.Is(e, keyring.ErrNotFound) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	b, e := base64.StdEncoding.DecodeString(v)
	return b, e == nil, e
}
func (WorkspaceKey) Set(key []byte) error {
	return keyring.Set("NyaDB SQL workspace", "master-v1", base64.StdEncoding.EncodeToString(key))
}
