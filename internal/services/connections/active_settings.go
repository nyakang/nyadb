package connections

import (
	"context"
	"fmt"
)

// ActiveSettings is the non-secret, immutable configuration that a future
// MySQL session factory will consume. Resolution never dials a server and
// never exposes a password, private-key content, or a raw connection string.
type ActiveSettings struct {
	ProfileID         int64
	ConnectionName    string
	ConfigurationName string // Empty denotes the primary configuration.
	Host              string
	Port              uint16
	Database          string
	Username          string
	TLSMode           string
	MySQLAdvanced     MySQLAdvanced
}

// ResolveActiveSettings resolves the persisted Active selection, NOT the
// configuration row currently highlighted in the editor. Corrupt or stale
// references fail closed rather than silently connecting to the main host.
func ResolveActiveSettings(profile Profile) (ActiveSettings, error) {
	if profile.Kind != MySQL {
		return ActiveSettings{}, fmt.Errorf("%w: active MySQL settings require a MySQL profile", ErrInvalid)
	}
	// normalize edits variant slices, so do not alias the repository's copy.
	profile.Variants = append([]ConnectionVariant(nil), profile.Variants...)
	var err error
	profile, err = normalize(profile)
	if err != nil {
		return ActiveSettings{}, err
	}
	result := ActiveSettings{
		ProfileID: profile.ID, ConnectionName: profile.Name,
		ConfigurationName: profile.ActiveVariant,
		Host:              profile.Host, Port: profile.Port, Database: profile.Database,
		Username: profile.Username, TLSMode: profile.TLSMode,
		MySQLAdvanced: profile.MySQLAdvanced,
	}
	if profile.ActiveVariant == "" {
		return result, nil
	}
	for _, variant := range profile.Variants {
		if variant.Name == profile.ActiveVariant {
			result.Host, result.Port = variant.Host, variant.Port
			result.Database, result.Username = variant.Database, variant.Username
			result.TLSMode, result.MySQLAdvanced = variant.TLSMode, variant.MySQLAdvanced
			return result, nil
		}
	}
	return ActiveSettings{}, fmt.Errorf("%w: active configuration missing", ErrInvalid)
}

// ActiveSettings prepares the settings selected by the saved Active marker.
// It intentionally makes no network request and does not cache credentials.
func (s *Service) ActiveSettings(ctx context.Context, profileID int64) (ActiveSettings, error) {
	profile, err := s.Get(ctx, profileID)
	if err != nil {
		return ActiveSettings{}, err
	}
	return ResolveActiveSettings(profile)
}
