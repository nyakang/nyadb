package kvworkspace

import (
	"encoding/json"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

type sessionDestroyImpactRecord struct {
	Scope struct {
		ProfileID, Generation, Catalog, Schema string
		HTTP                                   contract.KVHTTPIdentity
	}
	ID              string
	MaxRows         int
	MaxBytes, Bytes string
	Complete        bool
	Session, Root   json.RawMessage
	Keys            *HTTPSessionKeysReceipt
}

func destroyImpactRecord(o contract.KVHTTPSessionDestroyImpact) (sessionDestroyImpactRecord, error) {
	x := sessionDestroyImpactRecord{ID: o.Request.ID, MaxRows: o.Request.MaxRows, MaxBytes: strconv.FormatInt(o.Request.MaxBytes, 10), Bytes: strconv.FormatInt(o.Bytes, 10), Complete: o.Complete}
	x.Scope.ProfileID = strconv.FormatInt(o.Scope.Namespace.ProfileID, 10)
	x.Scope.Generation = strconv.FormatUint(o.Scope.Generation, 10)
	x.Scope.Catalog, x.Scope.Schema, x.Scope.HTTP = o.Scope.Namespace.Catalog, o.Scope.Namespace.Schema, o.Scope.HTTP
	var err error
	if o.Session.Request.Method != "" {
		x.Session, err = encodeOperationReceipt(Receipt{Scope: o.Scope.Clone(), Authored: o.Session.Request, Result: o.Session})
		if err != nil {
			return x, err
		}
	}
	if o.Keys.Root.Request.Method != "" {
		x.Keys = &HTTPSessionKeysReceipt{Request: o.Keys.Request, Matches: o.Keys.Matches, Bytes: o.Keys.Bytes, Complete: o.Keys.Complete, ProjectionComplete: o.Keys.ProjectionComplete}
		x.Root, err = encodeOperationReceipt(Receipt{Scope: o.Keys.Scope.Clone(), Authored: o.Keys.Root.Request, Result: o.Keys.Root, HTTPSessionKeys: x.Keys})
	}
	return x, err
}

func encodeHTTPSessionDestroyReceipt(r Receipt) ([]byte, error) {
	e := r.Result.HTTPSessionDestroy
	if e == nil {
		return nil, jsondocument.ErrInvalid
	}
	preview, err := destroyImpactRecord(e.Preview)
	if err != nil {
		return nil, err
	}
	recheck, err := destroyImpactRecord(e.Recheck)
	if err != nil {
		return nil, err
	}
	// Strip only the composite evidence from a copy before encoding the
	// original PUT. All native outcomes/body/header fields stay untouched.
	r.Result.HTTPSessionDestroy = nil
	write, err := encodeOperationReceipt(r)
	if err != nil {
		return nil, err
	}
	record := struct {
		Version             int    `json:"version"`
		LocalRecordID       string `json:"local_record_id"`
		Preview, Recheck    sessionDestroyImpactRecord
		Revalidated         bool            `json:"revalidated"`
		Write               json.RawMessage `json:"original_put"`
		WriteOutcomeUnknown bool            `json:"write_outcome_unknown"`
	}{1, e.LocalRecordID, preview, recheck, e.Revalidated, write, r.Result.WriteOutcomeUnknown}
	raw, err := json.MarshalIndent(record, "", "  ")
	if len(raw) > 256<<20 {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
