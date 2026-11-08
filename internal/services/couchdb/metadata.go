package couchdb

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const metadataLimit = 4096

func validDatabase(name string) bool {
	if name == "_users" || name == "_replicator" || name == "_global_changes" {
		return true
	}
	if name == "" || len(name) > 1024 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || strings.ContainsRune("_$()+/-", ch)) {
			return false
		}
	}
	return validateRequest(Request{Method: "GET", Path: "/" + url.PathEscape(name)}) == nil
}
func designPath(database, id string) (string, error) {
	if !strings.HasPrefix(id, "_design/") || len(id) <= 8 || len(id) > 4096 {
		return "", connections.ErrInvalid
	}
	path := "/" + url.PathEscape(database) + "/_design/" + url.PathEscape(strings.TrimPrefix(id, "_design/"))
	return path, validateRequest(Request{Method: "GET", Path: path})
}
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
	// Do not hide permission failures or guess an empty database list.
	response, err := c.Do(ctx, Request{Method: "GET", Path: "/_all_dbs"})
	if err != nil {
		return nil, err
	}
	var names []string
	if decodeJSON(response.Body, &names) != nil || names == nil {
		return nil, ErrProtocol
	}
	if len(names) > metadataLimit {
		return nil, ErrBounds
	}
	out := make([]contract.Namespace, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		if !validDatabase(name) || seen[name] {
			return nil, ErrProtocol
		}
		seen[name] = true
		out = append(out, contract.Namespace{Ref: s.Namespace(name), Label: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	return out, nil
}
func databaseInfo(ctx context.Context, c *Client, name string) ([]byte, error) {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(name)})
	if err != nil {
		return nil, err
	}
	var info struct {
		Name  string       `json:"db_name"`
		Count *json.Number `json:"doc_count"`
	}
	if decodeJSON(r.Body, &info) != nil || info.Name != name || info.Count == nil {
		return nil, ErrProtocol
	}
	if _, err = info.Count.Int64(); err != nil {
		return nil, ErrProtocol
	}
	return r.Body, nil
}
func designDefinition(raw []byte, id string) ([]contract.Definition, error) {
	var doc struct {
		ID    string                     `json:"_id"`
		Rev   string                     `json:"_rev"`
		Views map[string]json.RawMessage `json:"views"`
	}
	if decodeJSON(raw, &doc) != nil || doc.ID != id || doc.Rev == "" {
		return nil, ErrProtocol
	}
	out := []contract.Definition{{Name: id, Kind: "design_document", SQL: string(raw)}}
	names := make([]string, 0, len(doc.Views))
	for name := range doc.Views {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > metadataLimit {
		return nil, ErrBounds
	}
	for _, name := range names {
		out = append(out, contract.Definition{Name: name, Kind: "view", SQL: string(doc.Views[name])})
	}
	return out, nil
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
	info, err := databaseInfo(ctx, c, n.Catalog)
	if err != nil {
		return nil, err
	}
	out := []contract.DBObject{{Ref: n.Object("database", n.Catalog), Name: n.Catalog, Kind: "database", Engine: "Apache CouchDB", Definition: string(info)}}
	seen := map[string]bool{}
	last := ""
	bytes := len(info)
	for {
		path := "/" + url.PathEscape(n.Catalog) + "/_design_docs?include_docs=true&limit=257"
		if last != "" {
			key, _ := json.Marshal(last)
			path += "&startkey=" + url.QueryEscape(string(key))
		}
		response, err := c.Do(ctx, Request{Method: "GET", Path: path})
		if err != nil {
			return nil, err
		}
		var page struct {
			Rows []struct {
				ID  string          `json:"id"`
				Key string          `json:"key"`
				Doc json.RawMessage `json:"doc"`
			} `json:"rows"`
		}
		if decodeJSON(response.Body, &page) != nil || page.Rows == nil || len(page.Rows) > 257 {
			return nil, ErrProtocol
		}
		rows := page.Rows
		if last != "" && len(rows) > 0 && rows[0].Key == last {
			rows = rows[1:]
		}
		more := len(rows) > 256
		if len(rows) > 256 {
			rows = rows[:256]
		}
		for _, row := range rows {
			if row.ID != row.Key || seen[row.ID] || last != "" && row.Key <= last {
				return nil, ErrProtocol
			}
			if _, err = designPath(n.Catalog, row.ID); err != nil {
				return nil, ErrProtocol
			}
			if _, err = designDefinition(row.Doc, row.ID); err != nil {
				return nil, err
			}
			seen[row.ID] = true
			last = row.Key
			bytes += len(row.Doc)
			if len(seen) > metadataLimit || bytes > responseLimit {
				return nil, ErrBounds
			}
			out = append(out, contract.DBObject{Ref: n.Object("design_document", row.ID), Name: row.ID, Kind: "design_document", Engine: "Apache CouchDB", Definition: string(row.Doc)})
		}
		if !more {
			break
		}
	}
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return nil, err
	}
	var path string
	var err error
	switch o.Kind {
	case "database":
		if o.Name != o.Catalog {
			return nil, connections.ErrInvalid
		}
	case "design_document":
		path, err = designPath(o.Catalog, o.Name)
		if err != nil {
			return nil, err
		}
	default:
		return nil, contract.ErrUnsupported
	}
	c, ctx, done, err := s.metadataClient(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	var raw []byte
	var definitions []contract.Definition
	if o.Kind == "database" {
		raw, err = databaseInfo(ctx, c, o.Catalog)
		definitions = []contract.Definition{{Name: o.Name, Kind: "database", SQL: string(raw)}}
	} else {
		var response Response
		response, err = c.Do(ctx, Request{Method: "GET", Path: path})
		raw = response.Body
		if err == nil {
			definitions, err = designDefinition(raw, o.Name)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	return &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: o.Kind, Engine: "Apache CouchDB", Definition: string(raw), Definitions: definitions}, nil
}
