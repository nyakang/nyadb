package bootstrap

import (
	"context"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
)

func managedDriverPackages() connectioneditor.DriverPackages {
	resolve := func(kind connections.Kind, directory string) (*driverstore.Store, driverstore.Product, error) {
		if kind != connections.H2 && kind != connections.Trino && kind != connections.ZooKeeper && kind != connections.Databend {
			return nil, driverstore.Product{}, connections.ErrInvalid
		}
		product := driverstore.Product{Key: "h2", Runtime: "java", MainClass: "com.dbx.agent.h2.H2Agent"}
		if kind == connections.Databend {
			product = driverstore.Product{Key: "databend", Runtime: "java", MainClass: "com.dbx.agent.databend.DatabendAgent"}
		}
		if kind == connections.Trino {
			product = driverstore.Product{Key: "trino", Runtime: "java", MainClass: "com.dbx.agent.trino.TrinoAgent"}
		}
		if kind == connections.ZooKeeper {
			product = driverstore.Product{Key: "zookeeper", Runtime: "native"}
		}
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
		ImportJDBC: func(ctx context.Context, kind connections.Kind, directory string, request connections.JDBCImport) (connections.ManagedDriver, error) {
			store, product, err := resolve(kind, directory)
			if err != nil {
				return connections.ManagedDriver{}, err
			}
			if kind != connections.H2 || request.Profile != "h2-custom" {
				return connections.ManagedDriver{}, connections.ErrInvalid
			}
			return store.ImportJDBC(ctx, product, request)
		},
	}
}
