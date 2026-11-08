package meilisearch

import (
	"errors"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// Keep native protocol errors while sharing exact JSON validation with import.
func decodeJSON(raw []byte, target any) error {
	err := jsondocument.Decode(raw, target, responseLimit)
	if errors.Is(err, jsondocument.ErrBounds) {
		return ErrBounds
	}
	if err != nil {
		return ErrProtocol
	}
	return nil
}
