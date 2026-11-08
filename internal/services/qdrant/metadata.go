package qdrant

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const metadataLimit = 4096

func (s *Session) metadataClient(ctx context.Context) (*Client, context.Context, func(), error) {
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	c, err := s.open(ctx)
	if err != nil {
		done()
		return nil, nil, nil, err
	}
	return c, ctx, func() { c.Close(); done() }, nil
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	c, ctx, done, err := s.metadataClient(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	names, _, err := c.Collections(ctx)
	if err != nil {
		return nil, err
	}
	if len(names) > metadataLimit {
		return nil, ErrBounds
	}
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	out := make([]contract.Namespace, 0, len(names))
	for _, name := range names {
		out = append(out, contract.Namespace{Ref: s.Namespace(name), Label: name})
	}
	return out, nil
}

// Alias names are native indirections, not physical collection namespaces.
func physicalCollection(ctx context.Context, c *Client, name string) error {
	names, _, err := c.Collections(ctx)
	if err != nil {
		return err
	}
	if len(names) > metadataLimit {
		return ErrBounds
	}
	for _, actual := range names {
		if actual == name {
			return nil
		}
	}
	return connections.ErrInvalid
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.checkNamespace(n); err != nil {
		return nil, err
	}
	c, ctx, done, err := s.metadataClient(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err = physicalCollection(ctx, c, n.Catalog); err != nil {
		return nil, err
	}
	_, response, err := c.Collection(ctx, n.Catalog)
	if err != nil {
		return nil, err
	}
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	return []contract.DBObject{
		{Ref: n.Object("collection", n.Catalog), Name: n.Catalog, Kind: "集合信息", Engine: "Qdrant", Definition: string(response.Body)},
		{Ref: n.Object("points", "points"), Name: "points", Kind: "向量点", Engine: "Qdrant"},
		{Ref: n.Object("aliases", "aliases"), Name: "aliases", Kind: "集合别名", Engine: "Qdrant"},
	}, nil
}
func validCollectionAliases(raw []byte, collection string) bool {
	result, err := NativeResult(raw)
	var aliases struct {
		Aliases []struct {
			Name       string `json:"alias_name"`
			Collection string `json:"collection_name"`
		} `json:"aliases"`
	}
	if err != nil || decodeJSON(result, &aliases) != nil || aliases.Aliases == nil || len(aliases.Aliases) > metadataLimit {
		return false
	}
	seen := map[string]bool{}
	for _, alias := range aliases.Aliases {
		if _, err := collectionPath(alias.Name); err != nil || alias.Collection != collection || seen[alias.Name] {
			return false
		}
		seen[alias.Name] = true
	}
	return true
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return nil, err
	}
	switch o.Kind {
	case "collection":
		if o.Name != o.Catalog {
			return nil, connections.ErrInvalid
		}
	case "points", "aliases":
		if o.Name != o.Kind {
			return nil, connections.ErrInvalid
		}
	default:
		return nil, contract.ErrUnsupported
	}
	c, ctx, done, err := s.metadataClient(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err = physicalCollection(ctx, c, o.Catalog); err != nil {
		return nil, err
	}
	var response Response
	if o.Kind == "aliases" {
		path, _ := collectionPath(o.Catalog)
		response, err = c.Do(ctx, Request{Method: "GET", Path: path + "/aliases"})
		if err == nil && !validCollectionAliases(response.Body, o.Catalog) {
			err = ErrProtocol
		}
	} else {
		_, response, err = c.Collection(ctx, o.Catalog)
	}
	if err != nil {
		return nil, err
	}
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	return &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: o.Kind, Engine: "Qdrant", Definition: string(response.Body), Definitions: []contract.Definition{{Name: o.Name, Kind: o.Kind, SQL: string(response.Body)}}}, nil
}
