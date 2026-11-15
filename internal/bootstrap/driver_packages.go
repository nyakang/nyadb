package bootstrap

import (
	"context"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
)

func managedDriverPackages() connectioneditor.DriverPackages {
	resolve := func(kind connections.Kind, directory string) (*driverstore.Store, driverstore.Product, error) {
		if kind != connections.H2 {
			return nil, driverstore.Product{}, connections.ErrInvalid
		}
		product := driverstore.Product{Key: "h2", Runtime: "java", MainClass: "com.dbx.agent.h2.H2Agent"}
		var store *driverstore.Store
		var err error
		if directory == "" {
			store, err = driverstore.Default()
		} else {
			store, err = driverstore.New(directory)
		}
		return store, product, err
	}
	return connectioneditor.DriverPackages{
		List: func(ctx context.Context, kind connections.Kind, directory string) ([]connections.ManagedDriver, error) {
			store, product, err := resolve(kind, directory)
			if err != nil {
				return nil, err
			}
			return store.List(ctx, product)
		},
		Import: func(ctx context.Context, kind connections.Kind, directory, file string) (connections.ManagedDriver, error) {
			store, product, err := resolve(kind, directory)
			if err != nil {
				return connections.ManagedDriver{}, err
			}
			return store.Import(ctx, product, file)
		},
	}
}
