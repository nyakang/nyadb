package mongodb

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	names, err := s.client.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		return nil, safeError(err)
	}
	if s.profile.Database != "" {
		found := false
		for _, name := range names {
			if name == s.profile.Database {
				found = true
			}
		}
		if !found {
			names = append(names, s.profile.Database)
		}
	}
	sort.Strings(names)
	out := make([]contract.Namespace, 0, len(names))
	for _, n := range names {
		out = append(out, contract.Namespace{Ref: s.Namespace(n), Label: n})
	}
	return out, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if n.ProfileID != s.ProfileID() || n.Schema != "" || n.Catalog == "" {
		return nil, connections.ErrInvalid
	}
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	cur, err := s.client.Database(n.Catalog).ListCollections(ctx, bson.D{}, options.ListCollections().SetNameOnly(false))
	if err != nil {
		return nil, safeError(err)
	}
	defer cur.Close(ctx)
	var out []contract.DBObject
	for cur.Next(ctx) {
		var entry struct {
			Name    string   `bson:"name"`
			Type    string   `bson:"type"`
			Options bson.Raw `bson:"options"`
		}
		if cur.Decode(&entry) != nil {
			return nil, errors.New("MongoDB collection metadata could not be decoded")
		}
		kind, label := "collection", "集合"
		if entry.Type == "view" {
			kind, label = "view", "视图"
		}
		definition, err := extendedJSON(cur.Current)
		if err != nil {
			return nil, err
		}
		out = append(out, contract.DBObject{Ref: n.Object(kind, entry.Name), Name: entry.Name, Kind: label, Engine: "MongoDB", Definition: definition})
	}
	if err = cur.Err(); err != nil {
		return nil, safeError(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.object(o); err != nil {
		return nil, err
	}
	objects, err := s.Browse(ctx, o.Namespace())
	if err != nil {
		return nil, err
	}
	var match *contract.DBObject
	for i := range objects {
		if objects[i].Ref == o {
			match = &objects[i]
			break
		}
	}
	if match == nil {
		return nil, errors.New("MongoDB collection no longer exists")
	}
	out := &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: match.Kind, Engine: "MongoDB", Editable: o.Kind == "collection" && !strings.HasPrefix(o.Name, "system."), Columns: columns(), Definition: match.Definition, Definitions: []contract.Definition{{Name: o.Name, Kind: "collection", SQL: match.Definition}}}
	if o.Kind == "view" {
		return out, nil
	}
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	cur, err := s.client.Database(o.Catalog).Collection(o.Name).Indexes().List(ctx)
	if err != nil {
		return nil, safeError(err)
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var entry struct {
			Name   string `bson:"name"`
			Key    bson.D `bson:"key"`
			Unique bool   `bson:"unique"`
		}
		if cur.Decode(&entry) != nil {
			return nil, errors.New("MongoDB index metadata could not be decoded")
		}
		idx := contract.StructureIndex{Name: entry.Name, Type: "BSON", Unique: entry.Unique || entry.Name == "_id_"}
		for i, k := range entry.Key {
			direction := "ASC"
			if k.Value == int32(-1) || k.Value == int64(-1) {
				direction = "DESC"
			}
			idx.Columns = append(idx.Columns, contract.StructureIndexColumn{Name: k.Key, Direction: direction, Position: int64(i + 1)})
		}
		out.Indexes = append(out.Indexes, idx)
		definition, err := extendedJSON(cur.Current)
		if err != nil {
			return nil, err
		}
		out.Definitions = append(out.Definitions, contract.Definition{Name: entry.Name, Kind: "index", SQL: definition})
	}
	return out, safeError(cur.Err())
}
