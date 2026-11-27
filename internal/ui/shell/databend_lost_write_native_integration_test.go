package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func databendKillOriginalWorkbenchProcess(t *testing.T, f databendWorkbenchFixture) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.Directory, "processes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		PID        int    `json:"pid"`
		Executable string `json:"executable"`
		Start      string `json:"start_ticks"`
	}
	found := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var item = captured
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(item.Executable, filepath.Join(f.Directory, t.Name(), "store")+string(os.PathSeparator)) {
			captured = item
			found++
		}
	}
	if found != 1 || captured.PID <= 0 {
		t.Fatal("exactly one captured original fixture JVM required", found)
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", captured.PID))
	if err != nil || exe != captured.Executable {
		t.Fatal("captured original fixture executable changed", err)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", captured.PID))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	if len(fields) < 20 || fields[19] != captured.Start {
		t.Fatal("captured original process identity changed")
	}
	process, err := os.FindProcess(captured.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Kill(); err != nil {
		t.Fatal("original fixture process kill", err)
	}
	return captured.PID
}

func TestDatabendWorkbenchOriginalLostSessionRetainsWriteEvidenceAndNeverReplays(t *testing.T) {
	for _, mode := range []string{"runtime_disconnect", "process_exit"} {
		t.Run(mode, func(t *testing.T) {
			f := databendWorkbenchOriginal(t)
			table := "workbench_lost_" + mode + "55"
			databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".`+table+` (id UInt64 NOT NULL,txt String NOT NULL)`)
			databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (1,'first'),(2,'second')`)
			gate, endpoint := databendWorkbenchWriteGate(t, f, table, false, "workbench-lost-native-writes.jsonl")
			defer gate.unblock()
			acknowledged := make(chan agentbridge.Receipt, 4)
			s, tt, p, services := databendWorkbenchState(t, f, endpoint, func(method string, r agentbridge.Receipt) {
				if method == "data-write" {
					acknowledged <- r
				}
			})
			d := databendWorkbenchData(t, s, tt, *services, p, table)
			dataID, page, generation := s.currentTab().id, d.Page(), s.runtime.Generation(p.ID)
			s.openQuery()
			queryID, q := s.currentTab().id, s.currentTab().query
			databendWorkbenchWait(t, tt, func() bool { return !q.Preparing() && !q.Loading() })
			source := `INSERT INTO "` + table + `" VALUES (999,'must not replay')`
			q.Editor.Buffer.Set(source)
			s.selectTab(dataID)
			for row := range 2 {
				s.dataWorkspace.EditCell(*services, d, row, "txt", contract.Cell{Kind: contract.Text, Text: "changed"})
			}
			preview := databendWorkbenchPreview(t, s, tt, d)
			databendWorkbenchClick(t, tt, "确认并提交更改")
			databendAwaitReadGate(t, gate.held)
			finished := make(chan error, 1)
			pid := 0
			if mode == "runtime_disconnect" {
				// The external manager revokes publication immediately, then waits
				// for its captured original executor. Only its completion is shared.
				go func() { finished <- s.runtime.Disconnect(p.ID) }()
				deadline := time.Now().Add(10 * time.Second)
				for s.runtime.IsConnected(p.ID) {
					if time.Now().After(deadline) {
						t.Fatal("external original disconnect did not revoke publication")
					}
					time.Sleep(5 * time.Millisecond)
				}
				tt.Frame()
				if !d.Busy() || !d.HasDraft() || d.Page() != page || s.tabIndex(dataID) < 0 || !q.NeedsRecovery() || q.Editor.Buffer.String() != source {
					t.Fatal("external disconnect disposed original pending write or SQL")
				}
				select {
				case err := <-finished:
					t.Fatal("external disconnect returned before original executor", err)
				default:
				}
				gate.unblock()
				select {
				case err := <-finished:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(60 * time.Second):
					t.Fatal("external disconnect did not settle original executor")
				}
			} else {
				// Native HTTP already acknowledged a durable first write. Kill
				// only the captured JVM before its held reply reaches that owner.
				pid = databendKillOriginalWorkbenchProcess(t, f)
				gate.unblock()
			}
			databendWorkbenchWait(t, tt, func() bool { return !d.Busy() && !s.runtime.IsConnected(p.ID) && q.NeedsRecovery() })
			var original agentbridge.Receipt
			select {
			case original = <-acknowledged:
			case <-time.After(10 * time.Second):
				t.Fatal("lost-session original receipt missing")
			}
			want := contract.DataWriteAcknowledged
			if mode == "process_exit" {
				want = contract.DataWriteUnknown
			}
			if !original.Dispatched || mode == "runtime_disconnect" && (!original.ExecutorAcknowledged || original.ProcessExitConfirmed) || mode == "process_exit" && (original.ExecutorAcknowledged || !original.ProcessExitConfirmed) {
				t.Fatal("lost session substituted acknowledgement and process exit", original)
			}
			raw, receipt := databendWorkbenchWriteReceipt(t, tt)
			rows := databendWorkbenchSQL(t, f, `SELECT id,txt FROM "`+f.Database+`".`+table+` ORDER BY id`)
			if !d.HasDraft() || s.dataWorkspace.CanSave(d) || d.Page() != page || s.tabIndex(dataID) < 0 || gate.requests.Load() != 1 || len(receipt.Statements) != 2 || receipt.Statements[0].State != want || receipt.Statements[1].State != contract.DataWriteNotSent || len(rows) != 2 || rows[0][1] != "changed" || rows[1][1] != "second" || q.Editor.Buffer.String() != source {
				t.Fatal("lost session erased evidence, replayed suffix or lost SQL", raw, rows)
			}
			s.dataWorkspace.VerifySave(*services, d)
			s.dataWorkspace.Load(*services, d, true)
			tt.Frame()
			retained, _ := databendWorkbenchWriteReceipt(t, tt)
			if retained != raw || s.dataWorkspace.CanSave(d) || gate.requests.Load() != 1 {
				t.Fatal("late read replaced original lost-session evidence")
			}
			databendWorkbenchCapture(t, f, tt, "lost-write")
			databendWorkbenchClick(t, tt, "关闭原写入文档")
			databendWorkbenchClick(t, tt, "取消")
			if s.tabIndex(dataID) < 0 || !d.HasDraft() {
				t.Fatal("close cancellation discarded lost-session evidence")
			}
			databendWorkbenchClick(t, tt, "关闭原写入文档")
			databendWorkbenchClick(t, tt, "关闭并放弃本地草稿与证据")
			if s.tabIndex(dataID) >= 0 || d.Page() != nil {
				t.Fatal("explicit lost-session disposal retained original data")
			}
			s.selectTab(queryID)
			secret, err := os.ReadFile(f.PasswordFile)
			if err != nil {
				t.Fatal(err)
			}
			s.sessions.Current().Password = string(secret)
			s.sessions.Connect(p, services.Invalidate)
			databendWorkbenchWait(t, tt, func() bool { return s.runtime.IsConnected(p.ID) && s.sessions.StatusFor(p.ID) == "已连接" })
			databendWorkbenchClick(t, tt, "建立新物理会话（不执行现有 SQL）")
			databendWorkbenchWait(t, tt, func() bool { return !q.NeedsRecovery() && !q.Preparing() && !q.Loading() })
			recovered := databendWorkbenchSQL(t, f, `SELECT id,txt FROM "`+f.Database+`".`+table+` ORDER BY id`)
			if q.Result() != nil || q.Editor.Buffer.String() != source || gate.requests.Load() != 1 || s.runtime.Generation(p.ID) <= generation || len(recovered) != 2 || recovered[0][1] != "changed" || recovered[1][1] != "second" {
				t.Fatal("explicit original recovery replayed or changed SQL", recovered)
			}
			databendWorkbenchCapture(t, f, tt, "lost-recovered")
			databendWorkbenchJournal(t, f, "workbench-lost-write-lifecycle.jsonl", map[string]any{"test": t.Name(), "mode": mode, "object": d.Object(), "preview": json.RawMessage(preview), "receipt": json.RawMessage(raw), "rows": rows, "recovered_rows": recovered, "original_generation": generation, "replacement_generation": s.runtime.Generation(p.ID), "original_pid": pid, "writes": gate.requests.Load(), "draft_retained": true, "cache_preserved": true, "receipt_unchanged": true, "sql_text_retained": true, "recovery_replayed": false, "explicit_close_required": true, "public_provider_registered": false})
		})
	}
}
