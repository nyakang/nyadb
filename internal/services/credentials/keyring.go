// Package credentials stores database passwords in the operating system's
// credential vault, independently of the NyaDB SQLite profile database.
package credentials

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	keyring "github.com/zalando/go-keyring"
)

const serviceName = "NyaDB MySQL"

// Vault is the small, testable boundary for saved MySQL credentials.
// An empty variant denotes the primary connection configuration.
type Vault interface {
	Get(profileID int64, variant string) (password string, exists bool, err error)
	Set(profileID int64, variant, password string) error
	Delete(profileID int64, variant string) error
}

type OSVault struct{}

func NewOSVault() Vault { return OSVault{} }

func account(profileID int64, variant string) (string, error) {
	if profileID <= 0 {
		return "", fmt.Errorf("invalid profile identity")
	}
	// Identity is stable across connection-name, host, and username edits.
	// Encoding makes even arbitrary Unicode configuration names unambiguous.
	return strconv.FormatInt(profileID, 10) + "/" +
		base64.RawURLEncoding.EncodeToString([]byte(variant)), nil
}

func (OSVault) Get(profileID int64, variant string) (string, bool, error) {
	id, err := account(profileID, variant)
	if err != nil {
		return "", false, err
	}
	value, err := keyring.Get(serviceName, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read system credential vault: %w", err)
	}
	return value, true, nil
}

func (OSVault) Set(profileID int64, variant, password string) error {
	id, err := account(profileID, variant)
	if err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("refusing to store an empty password")
	}
	if err := keyring.Set(serviceName, id, password); err != nil {
		return fmt.Errorf("write system credential vault: %w", err)
	}
	return nil
}

func (OSVault) Delete(profileID int64, variant string) error {
	id, err := account(profileID, variant)
	if err != nil {
		return err
	}
	if err := keyring.Delete(serviceName, id); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete system credential: %w", err)
	}
	return nil
}
