package prestosql

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

func (p Provider) Open(ctx context.Context, profile connections.Profile, secret string) (contract.Session, error) {
	if ctx == nil || profile.ID <= 0 || profile.Kind != connections.PrestoSQL {
		return nil, connections.ErrInvalid
	}
	profile, err := connections.NormalizeProfile(profile)
	if err != nil {
		return nil, err
	}
	if profile.PrestoSQLOptions.DriverVersion == "" || profile.PrestoSQLOptions.DriverSHA256 == "" {
		return nil, ErrManagedDriver
	}
	config, err := connections.PrestoSQLProfileManagedConfig(profile)
	if err != nil {
		return nil, err
	}
	credentials, err := connections.DecodePrestoSQLCredentials(secret)
	if err != nil {
		return nil, err
	}
	if p.Store == nil {
		if profile.PrestoSQLOptions.DriverDirectory == "" {
			p.Store, err = driverstore.Default()
		} else {
			p.Store, err = driverstore.New(profile.PrestoSQLOptions.DriverDirectory)
		}
		if err != nil {
			return nil, ErrManagedDriver
		}
	}
	if p.Started == nil {
		p.Started = driverstore.RuntimeObserver(ctx)
	}
	session, err := p.OpenManaged(ctx, profile.ID, config, Credentials{Password: credentials.Password, Parameters: credentials.Parameters()})
	if err != nil {
		return nil, err
	}
	return session, nil
}
