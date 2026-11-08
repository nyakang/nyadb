package meilisearch

import (
	"net/url"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func serverBackupAction(action string) bool { return action == "dump" || action == "snapshot" }
func serverBackupRequest(action string) Request {
	path := "/dumps"
	if action == "snapshot" {
		path = "/snapshots"
	}
	return Request{Method: "POST", Path: path}
}
func serverBackupKind(request Request) string {
	u, err := url.Parse(request.Path)
	if err != nil || request.Method != "POST" {
		return ""
	}
	switch u.Path {
	case "/dumps":
		return "dumpCreation"
	case "/snapshots":
		return "snapshotCreation"
	}
	return ""
}
func (d *taskDocument) SupportsServerBackup(action string) bool {
	return serverBackupAction(action) && d.object.Catalog == "" && d.bound.s.config.IndexUID == "" && d.SupportsTaskManagement()
}
func (d *taskDocument) PrepareServerBackup(action string) (contract.TaskMutationPlan, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if !d.SupportsServerBackup(action) {
		return contract.TaskMutationPlan{}, contract.ErrUnsupported
	}
	if d.management.uncertain || d.management.receipt.Raw != "" {
		return contract.TaskMutationPlan{}, contract.ErrTaskMutationUsed
	}
	d.management.sequence++
	request := serverBackupRequest(action)
	p := contract.TaskMutationPlan{Object: d.object, Action: action, Request: request.Method + " " + request.Path, Sequence: d.management.sequence}
	p.Token = d.mutationPlanProof(p)
	return p, nil
}
func serverBackupArtifact(action string, raw []byte) (contract.TaskServerArtifact, error) {
	artifact := contract.TaskServerArtifact{Kind: action}
	fields, err := nativeObject(raw)
	if err != nil {
		return contract.TaskServerArtifact{}, err
	}
	// Native snapshots omit details. Do not invent a UID or server file path.
	if action == "snapshot" {
		return artifact, nil
	}
	details, err := nativeObject(fields["details"])
	if err != nil {
		return contract.TaskServerArtifact{}, ErrProtocol
	}
	uid, err := nativeString(details["dumpUid"])
	if err != nil || uid == "" || len(uid) > 128 || strings.Trim(uid, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return contract.TaskServerArtifact{}, ErrProtocol
	}
	artifact.UID = uid
	return artifact, nil
}

var _ contract.ServerBackupLease = (*taskDocument)(nil)
