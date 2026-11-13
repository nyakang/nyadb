package kvworkspace

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestHTTPHealthWatchRequestPresencePrecisionAndFileRoundTrip(t *testing.T) {
	text := `{"health":{"kind":"service","name":"原服务","passing":false,"filter":"","near":"_agent"},"start_index":"18446744073709551615","wait":"1s","max_observations":32,"max_bytes":"16777216"}`
	r, err := ParseHTTPWatchRequest(text)
	if err != nil || r.StartIndex != math.MaxUint64 || r.Health != (contract.KVHTTPHealthWatchTarget{Kind: "service", Name: "原服务", Passing: "false", IncludeFilter: true, IncludeNear: true, Near: "_agent"}) || r.Target != "" || r.Prefix {
		t.Fatal("health target lost option presence or precision", err, r)
	}
	raw, err := EncodeRequest(contract.KVRequest{Method: HTTPWatchMethod, JSON: text})
	if err != nil {
		t.Fatal(err)
	}
	request, err := DecodeRequest(raw)
	if err != nil || request.JSON != text || request.Method != HTTPWatchMethod {
		t.Fatal("health request file changed", err)
	}
	for _, invalid := range []string{
		strings.Replace(text, `"passing":false`, `"passing":null`, 1),
		strings.Replace(text, `"passing":false`, `"passing":"false"`, 1),
		strings.Replace(text, `"kind":"service"`, `"kind":"checks"`, 1),
		strings.Replace(text, `"kind":"service"`, `"kind":"catalog"`, 1),
		strings.Replace(text, `"name":"原服务"`, `"name":""`, 1),
		strings.Replace(text, `"filter":""`, `"filter":null`, 1),
		strings.Replace(text, `"start_index":"18446744073709551615"`, `"start_index":"18446744073709551616"`, 1),
		strings.Replace(text, `"health":`, `"target":"", "health":`, 1),
		strings.Replace(text, `"health":`, `"observation_prefix":"", "health":`, 1),
		strings.Replace(text, `"kind":"service"`, `"kind":"service","kind":"node"`, 1),
	} {
		if _, err := ParseHTTPWatchRequest(invalid); err == nil {
			t.Fatal("invalid or mixed target accepted", invalid)
		}
	}
	omitted := strings.Replace(text, `,"passing":false,"filter":"","near":"_agent"`, "", 1)
	r, err = ParseHTTPWatchRequest(omitted)
	if err != nil || r.Health.Passing != "" || r.Health.IncludeFilter || r.Health.IncludeNear {
		t.Fatal("omitted options became explicit", err)
	}
}

func TestHTTPHealthWatchToolGenerationCapturesOriginalEditorAndNeverReserves(t *testing.T) {
	s, d, lease := healthRequestToolsTestState(t)
	d.httpWatchAvailable = true
	kind := contract.KVToolHTTPHealthService
	s.OpenRequestTool(d, kind)
	input := contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{Service: "原服务", Passing: "false", IncludeFilter: true, IncludeNear: true, Index: "18446744073709551615", Wait: "1s"}}
	s.SetRequestToolInput(d, kind, input)
	original := d.Request()
	d.Editor.Buffer.Set("later editor")
	s.GenerateHTTPHealthWatch(d)
	if d.Request().JSON != "later editor" || !d.requestTools.open || d.Error() == "" {
		t.Fatal("generation replaced uncaptured editor")
	}
	s.OpenRequestTool(d, kind)
	s.GenerateHTTPHealthWatch(d)
	r, err := ParseHTTPWatchRequest(d.Request().JSON)
	if err != nil || d.Request().Method != HTTPWatchMethod || r.Health.Name != "原服务" || r.Health.Passing != "false" || !r.Health.IncludeFilter || !r.Health.IncludeNear || r.StartIndex != math.MaxUint64 || d.requestTools.open || d.Request() == original {
		t.Fatal("generation lost original health input", err)
	}
	if d.httpWatch.lease != nil || lease.executes.Load() != 0 || lease.connects.Load() != 0 || d.Busy() {
		t.Fatal("generation reserved or dispatched")
	}
}

func TestHTTPHealthWatchSelectedSnapshotBilingualBrowserRecordAndUnknownBarrier(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, p := httpWatchTest(t)
			d.scope.HTTP.Protocol = "consul-http"
			p.lease.scope = d.scope.Clone()
			r := healthBrowserReceipt(53, false)
			r.Scope = d.scope.Clone()
			r.Result.HTTP.Metadata.Index = new(uint64(0))
			unknown := &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
			d.unknown, d.last = unknown, healthBrowserReceipt(1, true)
			last := d.last
			request, err := httpHealthWatchToolRequest(contract.KVToolHTTPHealthState, contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{State: "any"}})
			if err != nil {
				t.Fatal(err)
			}
			d.Editor.Buffer.Set(request.JSON)
			p.lease.run.batch = contract.KVHTTPWatchBatch{Ended: true, Observations: []contract.KVHTTPWatchObservation{{HTTP: *r.Result.HTTP, ProjectionComplete: true, IndexRelation: "zero", IndexReset: true, ReceivedAt: time.Now()}}}
			close(p.lease.run.ack)
			s.StartHTTPWatch(d)
			consume(t, s, d)
			if d.httpWatch.healthReceipt == nil || d.httpWatch.healthReceipt == last || !d.httpWatch.indexReset || d.httpWatch.healthReceipt.Scope.Generation != d.scope.Generation {
				t.Fatal("selection lost original health snapshot")
			}
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			assertHTTPSessionRenewalControls(t, tt, size, "浏览所选健康观察", "复制完整 Watch 记录", "导出 Watch 记录")
			captureHealthUnit(t, tt, language, size, "health-watch-selected")
			workspaceClick(t, tt, "浏览所选健康观察")
			workspaceClick(t, tt, "下一页")
			workspaceClick(t, tt, "原检查 052")
			workspaceClick(t, tt, "复制所选原始 JSON")
			consume(t, s, d)
			want, err := originalHealthRow(r.Result.HTTP.Body, 52)
			if err != nil || tt.Clipboard() != string(want) {
				t.Fatal("selected snapshot copied unrelated one-shot receipt", err)
			}
			workspaceClick(t, tt, "查看所选原始详情")
			captureHealthUnit(t, tt, language, size, "health-watch-detail")
			if !strings.Contains(d.httpHealthBrowser.preview, "18446744073709551615") {
				t.Fatal("watch detail lost original uint64")
			}
			raw, err := s.HTTPWatchRecord(d)
			var record struct {
				Request struct {
					Health contract.KVHTTPHealthWatchTarget
				} `json:"captured_request"`
				Observations []struct {
					IndexReset bool `json:"index_reset"`
					Original   struct {
						HTTP struct{ Body []byte } `json:"http"`
					} `json:"original_http"`
				}
			}
			if err != nil || json.Unmarshal(raw, &record) != nil || record.Request.Health.Kind != "state" || !record.Observations[0].IndexReset || !bytes.Equal(record.Observations[0].Original.HTTP.Body, r.Result.HTTP.Body) || !bytes.Contains(record.Observations[0].Original.HTTP.Body, []byte("18446744073709551615")) {
				t.Fatal("complete watch record lost target, index reset or typed fields", err)
			}
			if d.last != last || d.unknown != unknown || !d.hasUnknownWrites() || p.reserves.Load() != 1 || p.lease.starts.Load() != 1 {
				t.Fatal("local snapshot inspection changed original receipt or dispatched")
			}
			workspaceClick(t, tt, "返回原 HTTP 回执")
			s.ClearHTTPWatch(d)
			if d.httpWatch.healthReceipt != nil || d.httpHealthBrowser != nil || d.httpHealth {
				t.Fatal("clear retained selected snapshot cache")
			}
			s.Close(d)
		}
	}
}

func TestHTTPHealthWatchObservationReplacementRevokesOriginalSelection(t *testing.T) {
	s, d, p := httpWatchTest(t)
	d.scope.HTTP.Protocol = "consul-http"
	p.lease.scope = d.scope.Clone()
	a, b := healthBrowserReceipt(2, false), healthBrowserReceipt(1, true)
	p.lease.run.batch = contract.KVHTTPWatchBatch{Ended: true, Observations: []contract.KVHTTPWatchObservation{{HTTP: *a.Result.HTTP, ProjectionComplete: true}, {HTTP: *b.Result.HTTP, ProjectionComplete: true}}}
	request, _ := httpHealthWatchToolRequest(contract.KVToolHTTPHealthState, contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{State: "any"}})
	d.Editor.Buffer.Set(request.JSON)
	close(p.lease.run.ack)
	s.StartHTTPWatch(d)
	consume(t, s, d)
	s.SelectHTTPWatchObservation(d, 0)
	original := s.healthBrowserReceipt(d)
	d.httpHealthBrowser = &httpHealthBrowserState{}
	d.httpHealthBrowser.bind(original)
	d.httpHealthBrowser.selectRow(original, 0, 1)
	s.SelectHTTPWatchObservation(d, 1)
	if d.httpHealthBrowser != nil || s.healthBrowserReceipt(d) == original || !reflect.DeepEqual(d.httpWatch.healthReceipt.Result.HTTP.Body, b.Result.HTTP.Body) {
		t.Fatal("replacement inherited selection from another original snapshot")
	}
	s.Close(d)
}
