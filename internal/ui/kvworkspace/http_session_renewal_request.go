package kvworkspace

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const HTTPSessionRenewalMethod = "/nyadb.KV/HTTPSessionRenewal"

type HTTPSessionRenewalInput struct {
	SessionID   string `json:"session_id"`
	Interval    string `json:"interval"`
	MaxRenewals string `json:"max_renewals"`
	MaxBytes    string `json:"max_bytes"`
}

func DefaultHTTPSessionRenewalRequest() contract.KVRequest {
	return encodeHTTPSessionRenewalInput(HTTPSessionRenewalInput{"", "2s", "16", "16777216"})
}
func encodeHTTPSessionRenewalInput(input HTTPSessionRenewalInput) contract.KVRequest {
	raw, _ := json.MarshalIndent(map[string]string{"session_id": input.SessionID, "interval": input.Interval, "max_renewals": input.MaxRenewals, "max_bytes": input.MaxBytes}, "", "  ")
	return contract.KVRequest{Method: HTTPSessionRenewalMethod, JSON: string(raw)}
}
func httpSessionRenewalInput(text string) (HTTPSessionRenewalInput, error) {
	var input HTTPSessionRenewalInput
	fields, err := eventFields(text, "session_id", "interval", "max_renewals", "max_bytes")
	if err != nil || len(fields) != 4 {
		return input, jsondocument.ErrInvalid
	}
	for key, dst := range map[string]*string{"session_id": &input.SessionID, "interval": &input.Interval, "max_renewals": &input.MaxRenewals, "max_bytes": &input.MaxBytes} {
		if key == "max_renewals" || key == "max_bytes" {
			if len(fields[key]) > 0 && fields[key][0] != '"' {
				*dst = string(fields[key])
				continue
			}
		}
		if json.Unmarshal(fields[key], dst) != nil {
			return input, jsondocument.ErrInvalid
		}
	}
	return input, nil
}
func ParseHTTPSessionRenewalRequest(text string) (contract.KVHTTPSessionRenewalRequest, error) {
	var r contract.KVHTTPSessionRenewalRequest
	input, err := httpSessionRenewalInput(text)
	if err != nil {
		return r, err
	}
	if len(input.SessionID) != 36 {
		return r, jsondocument.ErrInvalid
	}
	for i, c := range input.SessionID {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return r, jsondocument.ErrInvalid
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return r, jsondocument.ErrInvalid
		}
	}
	r.SessionID = input.SessionID
	if r.Interval, err = time.ParseDuration(input.Interval); err != nil || r.Interval < 100*time.Millisecond || r.Interval > time.Hour {
		return r, jsondocument.ErrInvalid
	}
	count, err := strconv.ParseUint(input.MaxRenewals, 10, 64)
	if err != nil || count < 1 || count > 256 {
		return r, jsondocument.ErrInvalid
	}
	r.MaxRenewals = int(count)
	budget, err := strconv.ParseUint(input.MaxBytes, 10, 64)
	if err != nil || budget < 1 || budget > 256<<20 {
		return r, jsondocument.ErrInvalid
	}
	r.MaxBytes = int64(budget)
	return r, nil
}
func (s *State) SetHTTPSessionRenewalInput(d *Document, input HTTPSessionRenewalInput) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.method != HTTPSessionRenewalMethod {
		return
	}
	r := encodeHTTPSessionRenewalInput(input)
	if r.JSON == d.Request().JSON {
		return
	}
	d.Editor.Buffer.Set(r.JSON)
	d.error = ""
	d.permit = nil
	d.httpSessionRenewalState().confirmation = nil
}
