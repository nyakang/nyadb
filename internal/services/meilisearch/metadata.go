package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const metadataLimit = 4096
const indexPageSize = 256

type indexInfo struct {
	UID, Created, Updated string
	PrimaryKey            *string
	Raw                   json.RawMessage
}

func parseIndex(raw []byte, expected string) (indexInfo, error) {
	f, err := nativeObject(raw)
	if err != nil {
		return indexInfo{}, err
	}
	uid, e1 := nativeString(f["uid"])
	created, e2 := nativeString(f["createdAt"])
	updated, e3 := nativeString(f["updatedAt"])
	_, e4 := time.Parse(time.RFC3339Nano, created)
	_, e5 := time.Parse(time.RFC3339Nano, updated)
	pk, present := f["primaryKey"]
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || !validIndex(uid) || expected != "" && uid != expected || len(created) > 64 || len(updated) > 64 || !present {
		return indexInfo{}, ErrProtocol
	}
	var primary *string
	if !bytes.Equal(pk, []byte("null")) {
		value, err := nativeString(pk)
		if err != nil || value == "" || len(value) > 1024 {
			return indexInfo{}, ErrProtocol
		}
		primary = &value
	}
	return indexInfo{uid, created, updated, primary, bytes.Clone(raw)}, nil
}
func readIndex(ctx context.Context, c *Client, uid string) (indexInfo, error) {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/indexes/" + uid})
	if err != nil {
		return indexInfo{}, err
	}
	return parseIndex(r.Body, uid)
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
	if s.config.IndexUID != "" {
		info, err := readIndex(ctx, c, s.config.IndexUID)
		if err != nil {
			return nil, err
		}
		if err = s.validateObservation(ctx, c); err != nil {
			return nil, err
		}
		return []contract.Namespace{{Ref: s.Namespace(info.UID), Label: info.UID}}, nil
	}
	out := []contract.Namespace{}
	seen := map[string]bool{}
	var total uint64
	bytesRead := 0
	for offset := uint64(0); ; {
		r, err := c.Do(ctx, Request{Method: "GET", Path: "/indexes?offset=" + strconv.FormatUint(offset, 10) + "&limit=256"})
		if err != nil {
			return nil, err
		}
		bytesRead += len(r.Body)
		if bytesRead > responseLimit {
			return nil, ErrBounds
		}
		f, err := nativeObject(r.Body)
		if err != nil {
			return nil, err
		}
		o, e1 := nativeUint(f["offset"], 64)
		l, e2 := nativeUint(f["limit"], 64)
		t, e3 := nativeUint(f["total"], 64)
		var rows []json.RawMessage
		if e1 != nil || e2 != nil || e3 != nil || o != offset || l != indexPageSize || decodeJSON(f["results"], &rows) != nil || rows == nil || len(rows) > indexPageSize {
			return nil, ErrProtocol
		}
		if t > metadataLimit {
			return nil, ErrBounds
		}
		if offset == 0 {
			total = t
		}
		// Offset pagination has no snapshot guard. Fail an observed changing
		// total, duplicate, short page or bad offset instead of claiming completeness.
		if t != total || offset > total || uint64(len(rows)) > total-offset || len(rows) == 0 && offset < total || len(rows) < indexPageSize && offset+uint64(len(rows)) < total {
			return nil, ErrProtocol
		}
		for _, raw := range rows {
			info, err := parseIndex(raw, "")
			if err != nil || seen[info.UID] {
				return nil, ErrProtocol
			}
			seen[info.UID] = true
			out = append(out, contract.Namespace{Ref: s.Namespace(info.UID), Label: info.UID})
		}
		offset += uint64(len(rows))
		if offset == total {
			break
		}
	}
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
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
	info, err := readIndex(ctx, c, n.Catalog)
	if err != nil {
		return nil, err
	}
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	// Settings and stats are independent explicit reads; their permissions do
	// not hide index information or fabricate an empty settings definition.
	return []contract.DBObject{
		{Ref: n.Object("index", n.Catalog), Name: n.Catalog, Kind: "索引信息", Engine: "Meilisearch", CreatedAt: info.Created, ModifiedAt: info.Updated, Definition: string(info.Raw)},
		{Ref: n.Object("documents", "documents"), Name: "documents", Kind: "文档", Engine: "Meilisearch"},
		{Ref: n.Object("settings", "settings"), Name: "settings", Kind: "索引设置", Engine: "Meilisearch"},
		{Ref: n.Object("statistics", "stats"), Name: "stats", Kind: "索引统计", Engine: "Meilisearch"},
		{Ref: n.Object("tasks", "tasks"), Name: "tasks", Kind: "任务", Engine: "Meilisearch"},
	}, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return nil, err
	}
	path := "/indexes/" + o.Catalog
	switch o.Kind {
	case "index":
		if o.Name != o.Catalog {
			return nil, connections.ErrInvalid
		}
	case "documents":
		if o.Name != "documents" {
			return nil, connections.ErrInvalid
		}
	case "settings":
		if o.Name != "settings" {
			return nil, connections.ErrInvalid
		}
		path += "/settings"
	case "statistics":
		if o.Name != "stats" {
			return nil, connections.ErrInvalid
		}
		path += "/stats"
	default:
		return nil, contract.ErrUnsupported
	}
	c, ctx, done, err := s.metadataClient(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	r, err := c.Do(ctx, Request{Method: "GET", Path: path})
	if err != nil {
		return nil, err
	}
	if o.Kind == "index" || o.Kind == "documents" {
		if _, err = parseIndex(r.Body, o.Catalog); err != nil {
			return nil, err
		}
	} else {
		if _, err = nativeObject(r.Body); err != nil {
			return nil, err
		}
	}
	if err = s.validateObservation(ctx, c); err != nil {
		return nil, err
	}
	return &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: o.Kind, Engine: "Meilisearch", Definition: string(r.Body), Definitions: []contract.Definition{{Name: o.Name, Kind: o.Kind, SQL: string(r.Body)}}}, nil
}
