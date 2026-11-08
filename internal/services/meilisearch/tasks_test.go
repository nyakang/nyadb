package meilisearch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mockTask(status string) string {
	raw := strings.Replace(mockReceipt, `"taskUid":`, `"uid":`, 1)
	return strings.Replace(raw, `"status":"enqueued"`, `"status":"`+status+`"`, 1)
}

func TestMeilisearchReceiptZeroUIDNullIndexAndFutureEvidence(t *testing.T) {
	for _, raw := range []string{mockReceipt, strings.Replace(mockReceipt, `"indexUid":"owned"`, `"indexUid":null`, 1)} {
		r, err := parseReceipt([]byte(raw))
		if err != nil || r.UID != 0 || string(r.Raw) != raw || r.Type != "documentAdditionOrUpdate" {
			t.Fatal("zero UID/native receipt lost", err)
		}
	}
	for _, raw := range []string{
		strings.Replace(mockReceipt, `"taskUid":0,`, "", 1),
		strings.Replace(mockReceipt, `"indexUid":"owned",`, "", 1),
		strings.Replace(mockReceipt, `"taskUid":0`, `"taskUid":null`, 1),
		strings.Replace(mockReceipt, `"taskUid":0`, `"taskUid":1.0`, 1),
		strings.Replace(mockReceipt, `"taskUid":0`, `"taskUid":4294967296`, 1),
		strings.Replace(mockReceipt, `"enqueued"`, `"succeeded"`, 1),
		strings.Replace(mockReceipt, `"enqueuedAt":"2026-11-08T08:05:30.372437829Z"`, `"enqueuedAt":"invalid"`, 1),
		strings.Replace(mockReceipt, `"indexUid":"owned"`, `"indexUid":"../owned"`, 1),
	} {
		if _, err := parseReceipt([]byte(raw)); !errors.Is(err, ErrProtocol) {
			t.Fatal("ambiguous admission accepted")
		}
	}
}

func TestMeilisearchSubmitAdmissionDoesNotWaitOrReportCompletion(t *testing.T) {
	var submits, reads atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			reads.Add(1)
			_, _ = io.WriteString(w, mockTask("succeeded"))
			return
		}
		submits.Add(1)
		w.WriteHeader(202)
		_, _ = io.WriteString(w, mockReceipt)
	})
	receipt, response, err := c.Submit(context.Background(), Request{Method: "POST", Path: "/indexes/owned/documents", Body: []byte(`[{"id":"one"}]`)})
	if err != nil || response.Status != 202 || receipt.UID != 0 || reads.Load() != 0 || submits.Load() != 1 {
		t.Fatal("admission confused with commit", err)
	}
	task, err := c.WaitTask(context.Background(), receipt, time.Second)
	if err != nil || task.Status != "succeeded" || reads.Load() != 1 || submits.Load() != 1 {
		t.Fatal("task wait submitted again", err)
	}
	if _, _, err := c.Submit(context.Background(), Request{Method: "GET", Path: "/tasks/0"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("readonly task submission accepted")
	}
}

func TestMeilisearchMalformedAdmissionRetiresWithoutReplay(t *testing.T) {
	for _, status := range []int{200, 202} {
		var calls atomic.Int32
		c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"status":"enqueued"}`)
		})
		request := Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"owned"}`)}
		_, response, err := c.Submit(context.Background(), request)
		var unknown *Interrupted
		if !errors.As(err, &unknown) || !unknown.WriteOutcomeUnknown || !errors.Is(err, ErrProtocol) || response.Status != status || len(response.Body) == 0 {
			t.Fatal("malformed accepted response lost outcome evidence")
		}
		if _, _, err := c.Submit(context.Background(), request); !errors.Is(err, ErrRetired) || calls.Load() != 1 {
			t.Fatal("malformed accepted request replayed")
		}
	}
}

func TestMeilisearchTaskMismatchAndFailurePreserveEvidence(t *testing.T) {
	receipt, _ := parseReceipt([]byte(mockReceipt))
	for _, change := range []func(string) string{
		func(s string) string { return strings.Replace(s, `"uid":0`, `"uid":1`, 1) },
		func(s string) string { return strings.Replace(s, `"indexUid":"owned"`, `"indexUid":"replacement"`, 1) },
		func(s string) string { return strings.Replace(s, "documentAdditionOrUpdate", "indexCreation", 1) },
		func(s string) string { return strings.Replace(s, "08:05:30", "08:05:31", 1) },
	} {
		raw := change(mockTask("succeeded"))
		c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, raw) })
		task, err := c.ReadTask(context.Background(), receipt)
		if !errors.Is(err, ErrTaskChanged) || string(task.Raw) != raw || !bytes.Equal(task.Receipt.Raw, receipt.Raw) {
			t.Fatal("original receipt mismatch ignored")
		}
	}
	for _, status := range []string{"failed", "canceled"} {
		raw := strings.TrimSuffix(mockTask(status), "}") + `,"error":{"message":"private-server-diagnostic","code":"missing_document_id","future":true}}`
		c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, raw) })
		task, err := c.WaitTask(context.Background(), receipt, time.Second)
		var failure *TaskFailure
		if !errors.As(err, &failure) || failure.Status != status || string(task.Raw) != raw || strings.Contains(err.Error(), "private-server-diagnostic") {
			t.Fatal("terminal failure evidence lost or leaked")
		}
	}
	changed := receipt
	changed.UID = 1
	if _, err := originalReceipt(changed); !errors.Is(err, ErrInvalid) {
		t.Fatal("mutable receipt tuple used without original evidence")
	}
}

func TestMeilisearchWaitCancelBoundAndCloseAcknowledgeOriginalObserver(t *testing.T) {
	receipt, _ := parseReceipt([]byte(mockReceipt))
	var reads atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		_, _ = io.WriteString(w, mockTask("processing"))
	})
	task, err := c.WaitTask(context.Background(), receipt, 300*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || task.Status != "processing" || reads.Load() < 1 {
		t.Fatal("bound lost last observation", err)
	}
	// An idle observer's timeout does not poison the transport or imply rollback.
	if _, err := c.ReadTask(context.Background(), receipt); err != nil {
		t.Fatal("idle wait timeout retired healthy client", err)
	}
	entered := make(chan struct{})
	blocked, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() })
	settled := make(chan error, 1)
	go func() { _, err := blocked.WaitTask(context.Background(), receipt, time.Minute); settled <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("wait observer did not enter")
	}
	if _, err := blocked.WaitTask(context.Background(), receipt, time.Second); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent polling worker admitted")
	}
	blocked.Close()
	select {
	case err := <-settled:
		if !errors.Is(err, ErrRetired) || !errors.Is(err, context.Canceled) {
			t.Fatal("Close did not cancel original observer", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not await original observer")
	}
	for _, bound := range []time.Duration{0, -1, 11 * time.Minute} {
		if _, err := c.WaitTask(context.Background(), receipt, bound); !errors.Is(err, ErrInvalid) {
			t.Fatal("unbounded observer accepted")
		}
	}
}
