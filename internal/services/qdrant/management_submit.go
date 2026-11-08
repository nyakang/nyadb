package qdrant

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type ManagedResult struct {
	State       string
	Receipt     ManagementReceipt
	Observation ManagementObservation
	Matched     bool
	States      map[string]string
}

func cloneManagementReceipt(r ManagementReceipt) ManagementReceipt {
	r.Response.Body = bytes.Clone(r.Response.Body)
	return r
}
func cloneManagedResult(r ManagedResult) ManagedResult {
	r.Receipt = cloneManagementReceipt(r.Receipt)
	r.Observation = cloneManagementObservation(r.Observation)
	copy := map[string]string{}
	for name, state := range r.States {
		copy[name] = state
	}
	r.States = copy
	return r
}
func (l *ManagementLease) keepResult(r ManagedResult) {
	if l.pending == nil {
		return
	}
	// The original receipt is retained once, independently of latest readback.
	r.Receipt = ManagementReceipt{}
	l.pending.last = cloneManagedResult(r)
}
func (l *ManagementLease) pendingResult() ManagedResult {
	r := ManagedResult{State: "indeterminate", States: map[string]string{}}
	if l.pending != nil {
		r = cloneManagedResult(l.pending.last)
		r.State = "indeterminate"
		r.Receipt = cloneManagementReceipt(l.pending.receipt)
	}
	return r
}

func (l *ManagementLease) Submit(ctx context.Context, p ManagedPlan) (ManagedResult, error) {
	result := ManagedResult{State: "not_submitted"}
	if !l.operation.TryLock() {
		return result, ErrBusy
	}
	defer l.operation.Unlock()
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if l.pending != nil {
		return l.pendingResult(), contract.ErrNamespaceBusy
	}
	if p.lease != l || p.sequence != l.planSequence || p.native.used == nil || p.native.used.Load() {
		return result, ErrManagementUsed
	}
	if err = l.validateBefore(p.before); err != nil {
		return result, err
	}
	if err = l.claim(p); err != nil {
		return result, err
	}
	l.planSequence++
	l.observation = nil
	defer func() {
		if l.pending == nil {
			l.releaseClaim()
		}
	}()
	c, err := l.session.open(ctx)
	if err != nil {
		return result, err
	}
	defer c.Close()
	observed, err := l.read(ctx, c, p.before.Targets)
	result.Observation = observed
	if err != nil {
		return result, err
	}
	if !managementPreflightMatches(p, observed) {
		return result, contract.ErrNamespaceChanged
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	l.pending = &managementPending{plan: p}
	defer func() { l.keepResult(result) }()
	receipt, err := c.SubmitManagement(ctx, p.native)
	l.pending.receipt = cloneManagementReceipt(receipt)
	result.Receipt = cloneManagementReceipt(receipt)
	var interrupted *Interrupted
	if err != nil && !errors.As(err, &interrupted) && receipt.Response.Status == 0 {
		// Native codec validation/busy/retired/already-canceled failures occur
		// before HTTP dispatch; the managed preview stays consumed.
		l.pending = nil
		return result, err
	}
	result.State = "uncertain"
	if receipt.HasResult {
		result.State = "response"
	}
	return result, err
}

// Verify opens only the captured session's endpoint and key. It never submits
// the original request again, invents a boolean receipt or selects a new session.
func (l *ManagementLease) Verify(ctx context.Context) (ManagedResult, error) {
	if !l.operation.TryLock() {
		return ManagedResult{}, ErrBusy
	}
	defer l.operation.Unlock()
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return l.pendingResult(), err
	}
	defer done()
	if l.pending == nil {
		return ManagedResult{}, ErrInvalid
	}
	result := l.pendingResult()
	result.Matched = false
	result.States = map[string]string{}
	defer func() { l.keepResult(result) }()
	c, err := l.session.open(ctx)
	if err != nil {
		return result, err
	}
	defer c.Close()
	p := l.pending.plan
	observed, err := l.read(ctx, c, p.before.Targets)
	result.Observation = observed
	if err != nil {
		return result, err
	}
	matched := true
	check := func(name string, ok bool) {
		state := "matched"
		if !ok {
			state = "diverged"
			matched = false
		}
		result.States[name] = state
	}
	for _, name := range p.claims {
		expected := slices.Contains(p.before.Collections, name)
		if p.native.action == "collection_create" {
			expected = true
		}
		if p.native.action == "collection_delete" {
			expected = false
		}
		check("collection/"+name, slices.Contains(observed.Collections, name) == expected)
	}
	if p.native.action == "aliases" {
		expected, _, err := simulateAliasChanges(p.before, p.aliases)
		if err != nil {
			return result, err
		}
		actual := managementAliasMap(observed)
		for _, name := range managedAliasNames(p, p.before) {
			check("alias/"+name, actual[name] == expected[name])
		}
	} else if p.native.action == "collection_delete" {
		for _, a := range observed.Aliases {
			if a.Collection == p.native.collection {
				check("alias/"+a.Alias, false)
			}
		}
	} else {
		for key, ok := range managementConfigMatches(p.native, p.before.Configs[p.native.collection], observed.Configs[p.native.collection]) {
			check("config/"+key, ok)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Matched = matched
	if !matched || !result.Receipt.HasResult {
		return result, nil
	}
	result.State = "unchanged"
	if result.Receipt.Result {
		result.State = "applied"
	}
	l.pending = nil
	l.observation = nil
	l.releaseClaim()
	return result, nil
}
