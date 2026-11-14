package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
)

type snapshotArchivePlan struct {
	owner  *Client
	used   *atomic.Bool
	target string
	limits snapshotArchiveLimits
}

// This private receipt deliberately does not use Response.Body or the ordinary
// document cache. Even a successful snapshot contains credentials and must only
// reach an explicitly owned private output. Disk fsync/publication are separate.
type snapshotArchiveCapture struct {
	target                                              string
	status                                              int
	protocol                                            string
	header, trailer                                     http.Header
	attempted, ended, eof, httpComplete, sourceComplete bool
	outputAcknowledged, canceled, completed             bool
	outputBytes                                         int64
	body                                                []byte
	proof                                               snapshotArchiveProof
}

func (r *snapshotArchiveCapture) clear() {
	clear(r.body)
	clear(r.proof.metadata)
	clear(r.proof.checksums)
	*r = snapshotArchiveCapture{}
}

func (c *Client) prepareSnapshotArchive(limits snapshotArchiveLimits) (snapshotArchivePlan, error) {
	if !limits.valid() {
		return snapshotArchivePlan{}, ErrInvalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.retired {
		return snapshotArchivePlan{}, ErrRetired
	}
	target, err := snapshotArchiveTarget(c.config)
	if err != nil {
		return snapshotArchivePlan{}, err
	}
	return snapshotArchivePlan{owner: c, used: &atomic.Bool{}, target: target, limits: limits}, nil
}

func snapshotArchiveTarget(cfg Config) (string, error) {
	// Only the measured native default/stale modes are admitted. A browsing
	// prefix never becomes a snapshot filter. Namespace and Partition never constrain the full backend.
	if cfg.Consistency == "consistent" {
		return "", ErrUnsupported
	}
	query := url.Values{}
	if cfg.Datacenter != "" {
		query.Set("dc", cfg.Datacenter)
	}
	if cfg.Consistency == "stale" {
		query.Set("stale", "")
	}
	target := cfg.Endpoint + "/v1/snapshot"
	if len(query) != 0 {
		target += "?" + query.Encode()
	}
	return target, nil
}

// The one original GET has no retry. Capture, parsing and synchronous private
// output acknowledgement all retain the original operation lock. Cancellation
// and Client.Close therefore wait for a blocked writer to return.
func (c *Client) captureSnapshotArchive(ctx context.Context, plan snapshotArchivePlan, output io.Writer) (result snapshotArchiveCapture, err error) {
	if plan.owner != c || plan.used == nil {
		return result, ErrPlanUsed
	}
	if output == nil {
		return result, ErrInvalid
	}
	if !c.operation.TryLock() {
		return result, ErrBusy
	}
	defer c.operation.Unlock()
	c.mu.Lock()
	unavailable, token := c.closed || c.retired, c.token
	c.mu.Unlock()
	if unavailable {
		return result, ErrRetired
	}
	if !plan.used.CompareAndSwap(false, true) {
		return result, ErrPlanUsed
	}
	result.target = plan.target
	defer func() { result.ended = true }()
	if err := ctx.Err(); err != nil {
		result.canceled = true
		return result, err
	}
	child, cancel := context.WithTimeout(ctx, c.config.RequestTimeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	interrupted := func(cause error) (snapshotArchiveCapture, error) {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		c.cancel()
		result.canceled = child.Err() != nil
		return result, cause
	}
	req, err := http.NewRequestWithContext(child, http.MethodGet, plan.target, nil)
	if err != nil {
		return result, ErrInvalid
	}
	req.Close = true
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	if c.config.Auth == "token" {
		req.Header.Set("X-Consul-Token", token)
	}
	result.attempted = true
	response, err := c.http.Do(req)
	if err != nil {
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return interrupted(errors.New("original Consul snapshot transport failed"))
	}
	result.status, result.protocol = response.StatusCode, response.Proto
	result.header = response.Header.Clone()
	// One extra byte detects an exceeded acceptance bound; incomplete original
	// prefixes remain private and never count as complete archives.
	result.body, err = io.ReadAll(io.LimitReader(response.Body, plan.limits.compressed+1))
	closeErr := response.Body.Close()
	result.trailer = response.Trailer.Clone()
	if int64(len(result.body)) > plan.limits.compressed {
		return interrupted(ErrBounds)
	}
	if err != nil {
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return interrupted(errors.New("original Consul snapshot body incomplete"))
	}
	result.eof = true
	if closeErr != nil {
		return interrupted(errors.New("original Consul snapshot body close failed"))
	}
	result.httpComplete = true
	if response.StatusCode != http.StatusOK {
		return result, &HTTPError{Status: response.StatusCode}
	}
	result.proof, err = inspectSnapshotArchive(child, result.body, plan.limits)
	if err != nil {
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return result, err
	}
	result.sourceComplete = true
	if child.Err() != nil {
		return interrupted(child.Err())
	}
	// Output cannot retain an alias into the original private receipt. A full
	// native archive does not imply that an external writer acknowledged it.
	copy := bytes.Clone(result.body)
	n, writeErr := output.Write(copy)
	clear(copy)
	if n < 0 || n > len(result.body) {
		return result, errors.New("original Consul snapshot output returned an invalid count")
	}
	result.outputBytes = int64(n)
	result.outputAcknowledged = n == len(result.body) && writeErr == nil
	if child.Err() != nil {
		return interrupted(child.Err())
	}
	if !result.outputAcknowledged {
		return result, errors.New("original Consul snapshot output incomplete")
	}
	result.completed = true
	return result, nil
}
