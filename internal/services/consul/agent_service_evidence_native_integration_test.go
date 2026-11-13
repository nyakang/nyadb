package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// This evidence probe uses only the owned fixture's captured transport. It
// does not enable a production endpoint, preparation, runtime or public tool.
func nativeAgentServiceEvidence(t *testing.T, c *Client, route string, body []byte) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.config.Endpoint+route, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody, req.Close = nil, true
	req.Header.Set("Content-Type", "application/json")
	if c.config.Auth == "token" {
		req.Header.Set("X-Consul-Token", c.token)
	}
	reply, err := c.http.Do(req)
	if err != nil {
		t.Fatal("original service evidence transport failed")
	}
	raw, err := io.ReadAll(io.LimitReader(reply.Body, responseLimit+1))
	closeErr := reply.Body.Close()
	if err != nil || closeErr != nil || len(raw) > responseLimit {
		t.Fatal("original service evidence response incomplete")
	}
	return Response{URL: req.URL.String(), Request: Request{Method: req.Method, Path: req.URL.EscapedPath(), Query: req.URL.Query(), Body: bytes.Clone(body)}, Status: reply.StatusCode, Protocol: reply.Proto, Header: reply.Header.Clone(), Trailer: reply.Trailer.Clone(), Body: raw, Attempted: true, Ended: true, Captured: true, EOF: true}
}

func nativeAgentServiceEvidenceJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func nativeAgentServiceEvidenceMaps(t *testing.T, c *Client, checks bool) map[string]json.RawMessage {
	t.Helper()
	build := AgentServicesRequest
	if checks {
		build = AgentChecksRequest
	}
	request, err := build(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := nativeDo(t, c, request)
	var values map[string]json.RawMessage
	if json.Unmarshal(r.Body, &values) != nil || values == nil {
		t.Fatal("original Agent map unavailable")
	}
	return values
}

func nativeAgentServiceEvidenceACK(t *testing.T, response Response, raw []byte) {
	t.Helper()
	if response.Status != 200 || len(response.Body) != 0 || !bytes.Equal(response.Request.Body, raw) || response.URL == "" || response.Protocol != "HTTP/1.1" {
		t.Fatal("original service success was not an empty complete HTTP response", response.Status, len(response.Body))
	}
}

func TestConsulNativeAgentServiceEvidenceOriginalUpdatesReplacementAndEmptyReceipts(t *testing.T) {
	c, _, _ := nativeClient(t)
	name := "owned-" + t.Name()
	id := name + "-service"
	deregister := "/v1/agent/service/deregister/" + url.PathEscape(id)
	registered := false
	t.Cleanup(func() {
		if registered {
			nativeAgentServiceEvidenceACK(t, nativeAgentServiceEvidence(t, c, deregister, nil), nil)
		}
	})
	payload := map[string]any{"ID": id, "Name": name, "Address": "127.0.0.1", "Port": 8087, "Tags": []string{"original", "中文"}, "Meta": map[string]string{"original": "原值"}, "Weights": map[string]uint64{"Passing": 3, "Warning": 2}, "EnableTagOverride": true}
	check := func(suffix string) map[string]any {
		return map[string]any{"CheckID": id + suffix, "Name": "original check", "TTL": "10m", "Status": "critical", "Notes": "original notes 中文"}
	}
	for _, step := range []struct {
		query, suffix string
		port          int
		checks        []string
	}{{"", "-a", 8087, []string{"-a"}}, {"", "-b", 8088, []string{"-a", "-b"}}, {"?replace-existing-checks=false", "-c", 8089, []string{"-a", "-b", "-c"}}, {"?replace-existing-checks=true", "-d", 8090, []string{"-d"}}, {"?replace-existing-checks=", "-e", 8091, []string{"-e"}}} {
		payload["Check"], payload["Port"] = check(step.suffix), step.port
		raw := nativeAgentServiceEvidenceJSON(t, payload)
		nativeAgentServiceEvidenceACK(t, nativeAgentServiceEvidence(t, c, "/v1/agent/service/register"+step.query, raw), raw)
		registered = true
		var service struct {
			ID, Service, Address string
			Port                 int
			Tags                 []string
			Meta                 map[string]string
		}
		if json.Unmarshal(nativeAgentServiceEvidenceMaps(t, c, false)[id], &service) != nil || service.ID != id || service.Service != name || service.Port != step.port || service.Address != "127.0.0.1" || len(service.Tags) != 2 || service.Meta["original"] != "原值" {
			t.Fatal("native service update changed original identity or fields")
		}
		values := nativeAgentServiceEvidenceMaps(t, c, true)
		for _, suffix := range []string{"-a", "-b", "-c", "-d", "-e"} {
			_, present := values[id+suffix]
			wanted := false
			for _, retained := range step.checks {
				wanted = wanted || suffix == retained
			}
			if present != wanted {
				t.Fatal("native check replacement semantics differ", step.query, suffix, present)
			}
		}
	}
	nativeAgentServiceEvidenceACK(t, nativeAgentServiceEvidence(t, c, deregister, nil), nil)
	registered = false
	if _, present := nativeAgentServiceEvidenceMaps(t, c, false)[id]; present {
		t.Fatal("deregister retained original service")
	}
	if _, present := nativeAgentServiceEvidenceMaps(t, c, true)[id+"-e"]; present {
		t.Fatal("deregister retained associated original check")
	}
}

func TestConsulNativeAgentServiceEvidenceOriginalEscapedIDsAndACLDenial(t *testing.T) {
	c, cfg, _ := nativeClient(t)
	name := "owned-" + t.Name()
	for _, suffix := range []string{"plain", "Unicode 雪", "percent%25", "delimiters?#++"} {
		id := name + "-" + suffix
		raw := nativeAgentServiceEvidenceJSON(t, map[string]any{"ID": id, "Name": name, "Port": 8097})
		register := nativeAgentServiceEvidence(t, c, "/v1/agent/service/register", raw)
		nativeAgentServiceEvidenceACK(t, register, raw)
		if _, present := nativeAgentServiceEvidenceMaps(t, c, false)[id]; !present {
			t.Fatal("registered native string ID was rewritten")
		}
		if token := os.Getenv("NYADB_CONSUL_READ_TOKEN"); token != "" {
			cfg.Auth = "token"
			limited, err := New(cfg, token)
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range []string{"/v1/agent/service/register", "/v1/agent/service/deregister/" + url.PathEscape(id)} {
				body := raw
				if strings.Contains(route, "/deregister/") {
					body = nil
				}
				denied := nativeAgentServiceEvidence(t, limited, route, body)
				if denied.Status != 403 || len(denied.Body) == 0 || !denied.Captured || !denied.EOF {
					t.Fatal("native ACL denial did not preserve a complete receipt", denied.Status)
				}
			}
			limited.Close()
			if _, present := nativeAgentServiceEvidenceMaps(t, c, false)[id]; !present {
				t.Fatal("denied deregistration changed the original service")
			}
		}
		removed := nativeAgentServiceEvidence(t, c, "/v1/agent/service/deregister/"+url.PathEscape(id), nil)
		nativeAgentServiceEvidenceACK(t, removed, nil)
		if _, present := nativeAgentServiceEvidenceMaps(t, c, false)[id]; present {
			t.Fatal("escaped original ID was not removed")
		}
	}
}
