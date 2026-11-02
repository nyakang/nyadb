package credentials

import (
	"errors"
	keyring "github.com/zalando/go-keyring"
)

// AISQLKeys has its own namespace and immutable random account identities.
// Existing connection, transport and workspace credential formats are unchanged.
type AISQLKeys struct{}

const aiSQLService = "NyaDB AI SQL v1"

func (AISQLKeys) Get(id string) (string, bool, error) {
	v, e := keyring.Get(aiSQLService, id)
	if errors.Is(e, keyring.ErrNotFound) {
		return "", false, nil
	}
	return v, e == nil, e
}
func (AISQLKeys) Set(id, value string) error { return keyring.Set(aiSQLService, id, value) }
func (AISQLKeys) Delete(id string) error {
	e := keyring.Delete(aiSQLService, id)
	if errors.Is(e, keyring.ErrNotFound) {
		return nil
	}
	return e
}
