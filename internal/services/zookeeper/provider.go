package zookeeper

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

// Provider shares the public profile path with the builtin provider. Started
// observes the captured original process; it cannot publish a session or replay.
type Provider struct{ Started func(int) error }

func OpenSession(ctx context.Context, profile connections.Profile, secret string) (contract.Session, error) {
	return (Provider{}).Open(ctx, profile, secret)
}

func (p Provider) Open(ctx context.Context, profile connections.Profile, secret string) (contract.Session, error) {
	profile, err := connections.NormalizeProfile(profile)
	if err != nil {
		return nil, err
	}
	c, err := connections.ZooKeeperProfileConfig(profile)
	if err != nil {
		return nil, err
	}
	if err = connections.ValidateZooKeeperCredential(profile, secret); err != nil {
		return nil, err
	}
	a := profile.ZooKeeperOptions
	if a.DriverVersion == "" || a.DriverSHA256 == "" {
		return nil, ErrInvalid
	}
	var store *driverstore.Store
	if a.DriverDirectory == "" {
		store, err = driverstore.Default()
	} else {
		store, err = driverstore.New(a.DriverDirectory)
	}
	if err != nil {
		return nil, err
	}
	list, err := store.List(ctx, driverstore.Product{Key: "zookeeper", Runtime: "native"})
	if err != nil {
		return nil, err
	}
	for _, d := range list {
		if d.Version == a.DriverVersion && d.SHA256 == a.DriverSHA256 && d.Supports("zookeeper-v1") {
			started := p.Started
			if started == nil {
				started = driverstore.RuntimeObserver(ctx)
			}
			return OpenNativeSession(ctx, profile.ID, c, secret, Options{Driver: d, MaxDocuments: a.MaxDocuments, Started: started})
		}
	}
	return nil, ErrInvalid
}
