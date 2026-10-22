package desktop

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"gitvault/internal/app"
)

func TestBoundedProgressBridge(t *testing.T) {
	events := app.NewEvents(4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s := app.ViewState{Repositories: []app.RepositoryView{{Name: "original"}}, Settings: app.SettingsDraft{CloudSecretKey: "synthetic-secret"}}
		for i := int64(1); i <= 10000; i++ {
			s.Revision = i
			events.Publish(s)
		}
		s.Repositories[0].Name = "mutated after publishing"
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("UI absence blocked publisher")
	}
	bridge := NewEventBackend(app.ViewState{}, events.Updates(), func(app.Command) error { return nil }, 64)
	s := bridge.Snapshot()
	if s.Revision != 10000 || len(s.Repositories) != 1 || s.Repositories[0].Name != "original" || s.Settings.CloudSecretKey != "" {
		t.Fatalf("coalesced event lost facts or leaked secret: %+v", s)
	}
	s.Repositories[0].Name = "changed by caller"
	if bridge.Snapshot().Repositories[0].Name != "original" {
		t.Fatal("presentation state aliases returned slice")
	}
}

func TestMainThreadDispatchNoWaitCycle(t *testing.T) {
	events := app.NewEvents(2)
	requests := make(chan app.Command, 1)
	stopped := make(chan struct{})
	bridge := NewEventBackend(app.ViewState{Connected: true, CanCancel: true}, events.Updates(), func(c app.Command) error { requests <- c; return nil }, 64)
	a := NewWithBackend(bridge)
	tt := ui.NewTester(a.View, 1000, 800)
	go func() {
		defer close(stopped)
		<-requests
		events.Publish(app.ViewState{Connected: true, Status: "Cancellation recorded", Notice: "Worker stopped without UI dispatch"})
	}()
	if err := tt.Click("Cancel operation"); err != nil {
		t.Fatal(err)
	}
	// Simulate a window closing: do not drain any UI events until the worker
	// terminates. A synchronous worker->RunOnMain call would deadlock here.
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("worker termination depended on main-thread drain")
	}
	tt.Frame()
	if !tt.HasText("Cancellation recorded") || !tt.HasText("Worker stopped without UI dispatch") {
		t.Fatalf("terminal event not applied on next UI frame: %v", tt.Texts())
	}
}

func TestLocalWorkflowCommands(t *testing.T) {
	t.Run("admission and busy", TestNativeLocalAdmissionAndBusyState)
	t.Run("restore exact input", TestNativeRestoreCarriesExactSnapshotAndDestination)
}
