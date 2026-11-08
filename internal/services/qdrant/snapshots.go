package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrSnapshotUsed     = errors.New("original Qdrant snapshot request already attempted; retain evidence without replay")
	ErrSnapshotChanged  = errors.New("original Qdrant snapshot description changed")
	ErrSnapshotChecksum = errors.New("Qdrant snapshot SHA256 mismatch")
	ErrSnapshotSource   = errors.New("Qdrant snapshot source is incomplete or differs from the prepared source")
	ErrSnapshotSink     = errors.New("Qdrant snapshot destination did not acknowledge the complete archive")
)

type SnapshotScope string

const (
	CollectionSnapshots SnapshotScope = "collection"
	ShardSnapshots      SnapshotScope = "shard"
	StorageSnapshots    SnapshotScope = "storage"
)

// Storage snapshots have no REST recovery endpoint. Recovery is offline startup.
type SnapshotTarget struct {
	Scope      SnapshotScope
	Collection string
	Shard      uint32
}

func (t SnapshotTarget) path() (string, error) {
	switch t.Scope {
	case StorageSnapshots:
		if t.Collection != "" || t.Shard != 0 {
			return "", ErrInvalid
		}
		return "/snapshots", nil
	case CollectionSnapshots, ShardSnapshots:
		path, err := collectionPath(t.Collection)
		if err != nil {
			return "", err
		}
		if t.Scope == CollectionSnapshots {
			if t.Shard != 0 {
				return "", ErrInvalid
			}
		} else {
			path += "/shards/" + strconv.FormatUint(uint64(t.Shard), 10)
		}
		return path + "/snapshots", nil
	default:
		return "", ErrInvalid
	}
}
func snapshotName(name string) (string, error) {
	if _, err := collectionPath(name); err != nil {
		return "", err
	}
	return url.PathEscape(name), nil
}
func validSnapshotChecksum(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// Raw preserves the full original native description. Size is never float64.
type SnapshotDescription struct {
	Name, CreationTime, Checksum string
	Size                         uint64
	HasCreationTime              bool
	Raw                          json.RawMessage
}

func cloneSnapshotDescription(d SnapshotDescription) SnapshotDescription {
	d.Raw = bytes.Clone(d.Raw)
	return d
}
func parseSnapshotDescription(raw []byte) (SnapshotDescription, error) {
	fields, err := object(raw)
	d := SnapshotDescription{}
	if err != nil || decodeJSON(fields["name"], &d.Name) != nil || bytes.Equal(bytes.TrimSpace(fields["size"]), []byte("null")) || decodeJSON(fields["size"], &d.Size) != nil {
		return d, ErrProtocol
	}
	if _, err = snapshotName(d.Name); err != nil {
		return d, ErrProtocol
	}
	if creation := bytes.TrimSpace(fields["creation_time"]); len(creation) == 0 {
		return d, ErrProtocol
	} else if !bytes.Equal(creation, []byte("null")) {
		if decodeJSON(creation, &d.CreationTime) != nil {
			return d, ErrProtocol
		}
		if _, e := time.Parse("2006-01-02T15:04:05.999999999", d.CreationTime); e != nil {
			return d, ErrProtocol
		}
		d.HasCreationTime = true
	}
	if checksum := bytes.TrimSpace(fields["checksum"]); len(checksum) > 0 && !bytes.Equal(checksum, []byte("null")) {
		if decodeJSON(checksum, &d.Checksum) != nil || !validSnapshotChecksum(d.Checksum) {
			return d, ErrProtocol
		}
	}
	d.Raw = bytes.Clone(raw)
	return d, nil
}
func validateSnapshotDescription(d SnapshotDescription) error {
	if _, err := snapshotName(d.Name); err != nil || d.Checksum != "" && !validSnapshotChecksum(d.Checksum) {
		return ErrInvalid
	}
	if d.HasCreationTime {
		if _, err := time.Parse("2006-01-02T15:04:05.999999999", d.CreationTime); err != nil {
			return ErrInvalid
		}
	} else if d.CreationTime != "" {
		return ErrInvalid
	}
	return nil
}
func (c *Client) ListSnapshots(ctx context.Context, target SnapshotTarget) ([]SnapshotDescription, Response, error) {
	path, err := target.path()
	if err != nil {
		return nil, Response{}, err
	}
	response, err := c.Do(ctx, Request{Method: "GET", Path: path})
	if err != nil {
		return nil, response, err
	}
	raw, err := NativeResult(response.Body)
	if err != nil {
		return nil, response, err
	}
	var rows []json.RawMessage
	if decodeJSON(raw, &rows) != nil || rows == nil || len(rows) > metadataLimit {
		return nil, response, ErrProtocol
	}
	out := make([]SnapshotDescription, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		d, err := parseSnapshotDescription(row)
		if err != nil || seen[d.Name] {
			return nil, response, ErrProtocol
		}
		seen[d.Name] = true
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b SnapshotDescription) int { return strings.Compare(a.Name, b.Name) })
	return out, response, nil
}

type SnapshotRecovery struct{ Location, Priority, Checksum, APIKey string }

func validateSnapshotRecovery(target SnapshotTarget, r SnapshotRecovery) error {
	if target.Scope == StorageSnapshots {
		return ErrInvalid
	}
	if r.Priority != "" && r.Priority != "no_sync" && r.Priority != "snapshot" && r.Priority != "replica" || r.Checksum != "" && !validSnapshotChecksum(r.Checksum) {
		return ErrInvalid
	}
	if r.Location == "" || len(r.Location) > 8192 || !utf8.ValidString(r.Location) || strings.IndexFunc(r.Location, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	u, err := url.Parse(r.Location)
	if err != nil || u.User != nil || u.Fragment != "" {
		return ErrInvalid
	}
	switch u.Scheme {
	case "http", "https":
		if u.Host == "" {
			return ErrInvalid
		}
	case "file":
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			return ErrInvalid
		}
	case "":
		if target.Scope != ShardSnapshots || strings.ContainsAny(r.Location, "?#") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if len(r.APIKey) > 4096 || !utf8.ValidString(r.APIKey) || strings.IndexFunc(r.APIKey, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	return nil
}

// A plan's copies share a one-use attempt, including across separately opened
// clients. Secret source keys are private and omitted from Preview and receipts.
// These native plans do not supply session claims or collection incarnation.
type SnapshotPlan struct {
	action  string
	target  SnapshotTarget
	request Request
	preview Request
	used    *atomic.Bool
	upload  *snapshotUploadPlan
}

func (p SnapshotPlan) Action() string         { return p.action }
func (p SnapshotPlan) Target() SnapshotTarget { return p.target }
func (p SnapshotPlan) Preview() Request       { r := p.preview; r.Body = bytes.Clone(r.Body); return r }
func PrepareSnapshotCreate(target SnapshotTarget, wait bool) (SnapshotPlan, error) {
	path, err := target.path()
	if err != nil {
		return SnapshotPlan{}, err
	}
	r := Request{Method: "POST", Path: path + "?wait=" + strconv.FormatBool(wait)}
	return SnapshotPlan{action: "create", target: target, request: r, preview: r, used: &atomic.Bool{}}, nil
}
func PrepareSnapshotDelete(target SnapshotTarget, name string, wait bool) (SnapshotPlan, error) {
	path, err := target.path()
	if err != nil {
		return SnapshotPlan{}, err
	}
	name, err = snapshotName(name)
	if err != nil {
		return SnapshotPlan{}, err
	}
	r := Request{Method: "DELETE", Path: path + "/" + name + "?wait=" + strconv.FormatBool(wait)}
	return SnapshotPlan{action: "delete", target: target, request: r, preview: r, used: &atomic.Bool{}}, nil
}
func PrepareSnapshotRecovery(target SnapshotTarget, recovery SnapshotRecovery, wait bool) (SnapshotPlan, error) {
	path, err := target.path()
	if err != nil {
		return SnapshotPlan{}, err
	}
	if err = validateSnapshotRecovery(target, recovery); err != nil {
		return SnapshotPlan{}, err
	}
	body := map[string]string{"location": recovery.Location}
	if recovery.Priority != "" {
		body["priority"] = recovery.Priority
	}
	if recovery.Checksum != "" {
		body["checksum"] = recovery.Checksum
	}
	if recovery.APIKey != "" {
		body["api_key"] = recovery.APIKey
	}
	raw, _ := json.Marshal(body)
	r := Request{Method: "PUT", Path: path + "/recover?wait=" + strconv.FormatBool(wait), Body: raw}
	delete(body, "api_key")
	preview, _ := json.Marshal(body)
	return SnapshotPlan{action: "recover", target: target, request: r, preview: Request{Method: r.Method, Path: r.Path, Body: preview}, used: &atomic.Bool{}}, nil
}

// State certifies only the original native response/admission. Recovery must
// still be checked against actual collection/shard data by its owning document.
type SnapshotReceipt struct {
	Action            string
	Target            SnapshotTarget
	State             string
	Response          Response
	HasResult, Result bool
	HasNullResult     bool
	Description       *SnapshotDescription
	SourceBytesRead   uint64
	SourceSHA256      string
}

func parseSnapshotReceipt(p SnapshotPlan, response Response) (SnapshotReceipt, error) {
	receipt := SnapshotReceipt{Action: p.action, Target: p.target, Response: response, State: "uncertain"}
	r := p.request
	// SourceBytesRead multipart requests have the same wait/native response semantics.
	state, err := requestOutcome(r, response)
	if err != nil {
		return receipt, err
	}
	if state == "enqueued" {
		receipt.State = state
		return receipt, nil
	}
	raw, err := NativeResult(response.Body)
	if err != nil {
		return receipt, err
	}
	if p.action == "create" {
		d, err := parseSnapshotDescription(raw)
		if err != nil {
			return receipt, err
		}
		receipt.Description = &d
	} else if p.action == "upload" && p.target.Scope == ShardSnapshots {
		// The native shard upload endpoint returns unit/null, unlike the
		// collection upload and shard URL recovery boolean receipts.
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return receipt, ErrProtocol
		}
		receipt.HasNullResult = true
	} else {
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) && !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) || decodeJSON(raw, &receipt.Result) != nil {
			return receipt, ErrProtocol
		}
		receipt.HasResult = true
	}
	receipt.State = "completed"
	return receipt, nil
}
func (c *Client) SubmitSnapshot(ctx context.Context, p SnapshotPlan) (SnapshotReceipt, error) {
	receipt := SnapshotReceipt{Action: p.action, Target: p.target, State: "not_submitted"}
	if p.used == nil || p.action == "" || p.upload != nil {
		return receipt, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if !p.used.CompareAndSwap(false, true) {
		return receipt, ErrSnapshotUsed
	}
	response, err := c.Do(ctx, p.request)
	receipt.Response = response
	if err != nil {
		if response.Status > 0 || errors.Is(err, ErrRetired) {
			receipt.State = "uncertain"
		}
		return receipt, err
	}
	receipt, err = parseSnapshotReceipt(p, response)
	if err != nil {
		c.retire()
		return receipt, &Interrupted{WriteOutcomeUnknown: true, Cause: err}
	}
	return receipt, nil
}

func (p SnapshotPlan) UploadSource() (name string, size uint64, checksum string, ok bool) {
	if p.upload == nil {
		return "", 0, "", false
	}
	return p.upload.name, p.upload.size, p.upload.checksum, true
}
