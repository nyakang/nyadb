package objectworkspace

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type namespaceTestLease struct {
	*taskManagementTestLease
	read func(context.Context, contract.NamespaceRef) (contract.NamespaceRecord, error)
}

func (l *namespaceTestLease) SupportsNamespaceManagement(string) bool { return true }
func (l *namespaceTestLease) ReadNamespace(c context.Context, r contract.NamespaceRef) (contract.NamespaceRecord, error) {
	return l.read(c, r)
}
func (l *namespaceTestLease) PrepareNamespaceMutation(a string, d contract.NamespaceDraft, b []contract.NamespaceRecord) (contract.TaskMutationPlan, error) {
	return contract.TaskMutationPlan{Action: a, Namespace: &contract.NamespaceMutation{Draft: d, Before: b, Versions: []uint64{4}}, Token: "namespace-plan", Request: "POST /indexes\n{\"uid\":\"owned\"}"}, nil
}
func namespaceTestState(t *testing.T) (*State, *Session, *Document, *namespaceTestLease) {
	s, current, d, base := taskManagementTestState(t)
	l := &namespaceTestLease{taskManagementTestLease: base, read: func(_ context.Context, r contract.NamespaceRef) (contract.NamespaceRecord, error) {
		return contract.NamespaceRecord{Ref: r, Version: 4, Raw: `{"native":9007199254740993}`, StatsRaw: `{"numberOfDocuments":0}`, Token: "namespace-proof"}, nil
	}}
	d.tasks.lease = l
	d.tasks.namespace = namespaceManagementState{action: "namespace_create", uid: "owned", open: true}
	return s, current, d, l
}
func TestNamespaceFormParkedReadsDraftChangesGenerationAndCancellation(t *testing.T) {
	for _, outcome := range []string{"parked", "changed", "generation", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			s, current, d, l := namespaceTestState(t)
			entered, release := make(chan struct{}), make(chan struct{})
			read := l.read
			l.read = func(c context.Context, r contract.NamespaceRef) (contract.NamespaceRecord, error) {
				close(entered)
				<-release
				return read(c, r)
			}
			s.ReadNamespaceManagement(d, nil)
			<-entered
			switch outcome {
			case "parked":
				current.ProfileID = 9
			case "changed":
				d.tasks.namespace.uid = "other"
			case "generation":
				current.Generation++
			case "cancel":
				s.CancelTasks(d)
				if !d.Loading() {
					t.Fatal("cancel skipped acknowledgement")
				}
			}
			close(release)
			if outcome == "parked" {
				time.Sleep(10 * time.Millisecond)
				s.Consume(ui.Services{})
				if !d.Loading() || len(d.tasks.namespace.records) != 0 {
					t.Fatal("parked namespace read consumed")
				}
				current.ProfileID = d.profileID
			}
			waitTasks(t, s, d)
			if outcome == "parked" {
				if len(d.tasks.namespace.records) != 1 || d.tasks.namespace.records[0].Ref.Catalog != "owned" {
					t.Fatal("original buffered snapshot lost")
				}
			} else if len(d.tasks.namespace.records) != 0 {
				t.Fatal("changed/revoked/canceled namespace read accepted")
			}
		})
	}
}
func TestNamespaceSubmissionOriginalHostGuardDeepCopiesAndParkedEvidence(t *testing.T) {
	s, current, d, l := namespaceTestState(t)
	s.ReadNamespaceManagement(d, nil)
	waitTasks(t, s, d)
	if e := s.PrepareNamespaceManagement(d); e != nil {
		t.Fatal(e)
	}
	if s.canSubmitTaskPlan(d) {
		t.Fatal("untyped namespace confirmation accepted")
	}
	d.tasks.namespace.confirmation = "owned"
	var continuation func(ui.Services)
	s.deps.BeforeNamespace = func(_ ui.Services, id uint64, p contract.TaskMutationPlan, next func(ui.Services)) bool {
		if id != d.id || p.Namespace.Draft.Ref.Catalog != "owned" {
			t.Fatal("host guard lost scope")
		}
		continuation = next
		return true
	}
	entered, release := make(chan struct{}), make(chan struct{})
	l.mutate = func(_ context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
		close(entered)
		<-release
		if p.Namespace.Before[0].Version != 4 || p.Namespace.Versions[0] != 4 {
			t.Error("worker captured mutable namespace arrays")
		}
		return contract.TaskMutationResult{State: "enqueued", Namespace: contract.CloneNamespaceMutation(p.Namespace), Receipt: contract.TaskMutationReceipt{Raw: "native admission", Namespace: contract.CloneNamespaceMutation(p.Namespace)}}, nil
	}
	callbacks := 0
	s.deps.NamespaceChanged = func(_ ui.Services, e NamespaceEvent) {
		callbacks++
		if e.Object.ProfileID != d.profileID || e.Generation != d.tasks.generation || e.Result.Namespace.Draft.Ref.Catalog != "owned" {
			t.Fatal("host completion rebound")
		}
	}
	s.SubmitTaskManagement(d, nil, ui.Services{})
	if continuation == nil || d.Loading() {
		t.Fatal("namespace submission bypassed host guard")
	}
	p := d.tasks.management.plan
	continuation(ui.Services{})
	<-entered
	p.Namespace.Before[0].Version = 99
	p.Namespace.Versions[0] = 99
	current.ProfileID = 9
	close(release)
	time.Sleep(10 * time.Millisecond)
	s.Consume(ui.Services{})
	if !d.Loading() || callbacks != 0 {
		t.Fatal("parked namespace admission consumed")
	}
	current.ProfileID = d.profileID
	waitTasks(t, s, d)
	if callbacks != 1 || s.TaskManagementResult(d).Namespace.Before[0].Version != 4 {
		t.Fatal("original scope evidence lost")
	}
	r := s.TaskManagementResult(d)
	r.Namespace.Before[0].Version = 88
	r.Receipt.Namespace.Versions[0] = 88
	if s.TaskManagementResult(d).Namespace.Before[0].Version != 4 || s.TaskManagementResult(d).Receipt.Namespace.Versions[0] != 4 {
		t.Fatal("namespace evidence exposed mutable arrays")
	}
	if !s.GuardDesignLeave(d, nil) {
		t.Fatal("namespace receipt leave unguarded")
	}
	raw := string(s.DocumentSnapshot(d))
	if strings.Contains(raw, "native admission") || strings.Contains(raw, "namespace-proof") {
		t.Fatal("namespace evidence persisted")
	}
}
func TestNamespaceUnadmittedOperationDoesNotClaimRetainedReceipt(t *testing.T) {
	for _, state := range []string{"not_submitted", "rejected"} {
		t.Run(state, func(t *testing.T) {
			s, _, d, l := namespaceTestState(t)
			s.ReadNamespaceManagement(d, nil)
			waitTasks(t, s, d)
			if e := s.PrepareNamespaceManagement(d); e != nil {
				t.Fatal(e)
			}
			d.tasks.namespace.confirmation = "owned"
			l.mutate = func(_ context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
				return contract.TaskMutationResult{State: state, Request: p.Request, Namespace: contract.CloneNamespaceMutation(p.Namespace)}, contract.ErrNamespaceChanged
			}
			s.SubmitTaskManagement(d, nil)
			waitTasks(t, s, d)
			d.tasks.tab = 3
			tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
			if tt.HasText("索引任务与元数据尚未共同确认；回执保留，禁止重放。") {
				t.Fatal("unadmitted operation claims a receipt")
			}
			if s.TaskManagementUncertain(d) || d.tasks.management.receipt.Raw != "" || !s.CanManageNamespace(d, "namespace_create") {
				t.Fatal("known rejection became pending admission")
			}
		})
	}
}
