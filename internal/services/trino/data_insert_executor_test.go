package trino

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestInsertAcknowledgementRequiresOriginalTerminalReceipt(t *testing.T) {
	for _, test := range []struct {
		name, tail string
		state      contract.DataWriteStatementState
	}{
		{"ack", `"updateType":"INSERT","updateCount":1`, contract.DataWriteAcknowledged},
		{"no-count", `"updateType":"INSERT"`, contract.DataWriteUnknown},
		{"no-type", `"updateCount":1`, contract.DataWriteUnknown},
		{"negative-count", `"updateType":"INSERT","updateCount":-1`, contract.DataWriteUnknown},
		{"failure", `"error":{"errorCode":1,"errorName":"NOT_SUPPORTED","errorType":"USER_ERROR"}`, contract.DataWriteFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprintf(w, `{"id":%q,%s}`, testID, test.tail)
			})
			r, err := c.insert(context.Background(), "INSERT INTO native VALUES(1)")
			if r.State != test.state || calls.Load() != 1 || !json.Valid([]byte(r.NativeJSON)) {
				t.Fatal(r, err, calls.Load())
			}
			if test.state != contract.DataWriteAcknowledged && err == nil {
				t.Fatal("incomplete native receipt has no error")
			}
		})
	}
}
func TestInsertCanceledBeforeDispatchHasNoWriteReceipt(t *testing.T) {
	var calls atomic.Int64
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, err := c.insert(ctx, "INSERT INTO native VALUES(1)")
	if err == nil || a.State != contract.DataWriteNotSent || a.NativeJSON != "" || calls.Load() != 0 {
		t.Fatal("pre-dispatch cancellation invented a receipt", a, err)
	}
}
