package meilisearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func backupMockLease(t *testing.T, action, fault string) (*Session, *taskDocument, *atomic.Int32) {
	t.Helper()
	writes := &atomic.Int32{}
	kind := serverBackupKind(serverBackupRequest(action))
	c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes.Add(1)
			if !strings.HasSuffix(r.URL.Path, serverBackupRequest(action).Path) || r.URL.RawQuery != "" {
				t.Error("backup scope changed")
			}
			if fault == "lost" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			if fault == "rejected" {
				w.WriteHeader(403)
				_, _ = io.WriteString(w, `{"code":"invalid_api_key","type":"auth","message":"denied"}`)
				return
			}
			raw := strings.ReplaceAll(strings.ReplaceAll(mockReceipt, `"indexUid":"owned"`, `"indexUid":null`), "documentAdditionOrUpdate", kind)
			if fault == "wrong_kind" {
				raw = strings.ReplaceAll(raw, kind, "indexCreation")
			}
			if fault == "wrong_scope" {
				raw = strings.ReplaceAll(raw, `"indexUid":null`, `"indexUid":"owned"`)
			}
			w.WriteHeader(202)
			_, _ = io.WriteString(w, raw)
			return
		}
		raw := strings.ReplaceAll(strings.ReplaceAll(taskDocumentFixtureRaw(0, "succeeded"), `"indexUid":"owned"`, `"indexUid":null`), "documentAdditionOrUpdate", kind)
		details := `"details":{"dumpUid":"20261108-123456789","future":9007199254740993}`
		if action == "snapshot" {
			details = `"details":null`
		}
		if fault == "bad_details" {
			details = `"details":{"dumpUid":"../unsafe"}`
		}
		raw = strings.ReplaceAll(raw, `"details":{"future":9007199254740993,"text":"原生🐱"}`, details)
		if fault == "failed" {
			raw = strings.ReplaceAll(raw, `"status":"succeeded"`, `"status":"failed"`)
		}
		_, _ = io.WriteString(w, raw)
	})
	c.Close()
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
	lease, err := s.ReserveTaskDocument(s.Namespace("").Object("tasks", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	d := lease.(*taskDocument)
	t.Cleanup(func() { _ = d.Close() })
	return s, d, writes
}

func TestServerBackupGlobalPureOneUseAdmissionAndRecoveredArtifact(t *testing.T) {
	for _, action := range []string{"dump", "snapshot"} {
		t.Run(action, func(t *testing.T) {
			s, d, writes := backupMockLease(t, action, "")
			scoped, err := s.ReserveTaskDocument(s.Namespace("owned").Object("tasks", "tasks"))
			if err != nil {
				t.Fatal(err)
			}
			defer scoped.Close()
			if scoped.(contract.ServerBackupLease).SupportsServerBackup(action) {
				t.Fatal("index backup capability")
			}
			if _, err = scoped.(contract.ServerBackupLease).PrepareServerBackup(action); !errors.Is(err, contract.ErrUnsupported) {
				t.Fatal("index backup prepared", err)
			}
			plan, err := d.PrepareServerBackup(action)
			if err != nil || writes.Load() != 0 || len(plan.Records) != 0 || plan.Request != "POST "+serverBackupRequest(action).Path {
				t.Fatal("preview scope or I/O", err)
			}
			forged := plan
			forged.Action = "delete"
			if _, err = d.MutateTasks(context.Background(), forged); !errors.Is(err, contract.ErrTaskMutationUsed) || writes.Load() != 0 {
				t.Fatal("forged preview dispatched", err)
			}
			old := plan
			plan, err = d.PrepareServerBackup(action)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.MutateTasks(context.Background(), old); !errors.Is(err, contract.ErrTaskMutationUsed) {
				t.Fatal("replaced plan dispatched", err)
			}
			admitted, err := d.MutateTasks(context.Background(), plan)
			if err != nil || admitted.State != "enqueued" || admitted.CountsKnown || admitted.Artifact.Kind != "" || admitted.Receipt.Type != serverBackupKind(serverBackupRequest(action)) || writes.Load() != 1 {
				t.Fatal("admission claims artifact", err, admitted)
			}
			if _, err = d.PrepareServerBackup(action); !errors.Is(err, contract.ErrTaskMutationUsed) {
				t.Fatal("pending backup replay", err)
			}
			recovered, err := s.RecoverTaskDocument(d.object, d)
			if err != nil || recovered.Receipt.Raw != admitted.Receipt.Raw || recovered.Receipt.Token == admitted.Receipt.Token {
				t.Fatal("recovery lost evidence", err)
			}
			defer recovered.Lease.Close()
			settled, err := recovered.Lease.(contract.TaskManagementLease).ObserveTaskMutation(context.Background(), recovered.Receipt, time.Second)
			if err != nil || settled.State != "succeeded" || settled.Artifact.Kind != action || settled.CountsKnown || writes.Load() != 1 {
				t.Fatal("original artifact lost", err, settled)
			}
			if action == "dump" && settled.Artifact.UID != "20261108-123456789" || action == "snapshot" && settled.Artifact.UID != "" {
				t.Fatal("fabricated artifact", settled.Artifact)
			}
			if _, err = recovered.Lease.(contract.TaskManagementLease).MutateTasks(context.Background(), plan); !errors.Is(err, contract.ErrTaskMutationUsed) {
				t.Fatal("recovered preview replayed", err)
			}
		})
	}
}
func TestServerBackupUnknownMalformedAdmissionFailureAndInvalidArtifact(t *testing.T) {
	for _, fault := range []string{"lost", "wrong_kind", "wrong_scope", "rejected", "failed", "bad_details"} {
		t.Run(fault, func(t *testing.T) {
			s, d, writes := backupMockLease(t, "dump", fault)
			plan, err := d.PrepareServerBackup("dump")
			if err != nil {
				t.Fatal(err)
			}
			admitted, err := d.MutateTasks(context.Background(), plan)
			switch fault {
			case "lost", "wrong_kind", "wrong_scope":
				if err == nil || admitted.State != "uncertain" {
					t.Fatal("unknown admission lost", err, admitted)
				}
				recovered, err := s.RecoverTaskDocument(d.object, d)
				if err != nil || !recovered.Uncertain {
					t.Fatal("uncertainty lost", err)
				}
				defer recovered.Lease.Close()
				if _, err = recovered.Lease.(contract.ServerBackupLease).PrepareServerBackup("dump"); !errors.Is(err, contract.ErrTaskMutationUsed) {
					t.Fatal("unknown creation became replay", err)
				}
			case "rejected":
				if err == nil || admitted.State != "rejected" || d.management.uncertain {
					t.Fatal("native refusal lost", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				settled, err := d.ObserveTaskMutation(context.Background(), admitted.Receipt, time.Second)
				if settled.Artifact.Kind != "" {
					t.Fatal("invalid backup advertised", settled)
				}
				if fault == "failed" && (err != nil || settled.State != "failed" || d.management.receipt.Raw != "") {
					t.Fatal("failure lost", err)
				}
				if fault == "bad_details" && (err == nil || d.management.receipt.Raw == "") {
					t.Fatal("invalid artifact settled", err)
				}
			}
			if writes.Load() != 1 {
				t.Fatal("creation replayed")
			}
		})
	}
}
