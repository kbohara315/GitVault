package desktop

import "gitvault/internal/app"

// EventBackend is owned by the UI thread. Snapshot applies at most one bounded
// batch; no worker dispatches synchronous RunOnMain calls or mutates its cache.
// Channels must contain full snapshots, not deltas: saturated progress may be
// coalesced by the app's Events mailbox.
type EventBackend struct {
	current   app.ViewState
	updates   <-chan app.ViewState
	submit    func(app.Command) error
	batchSize int
}

func NewEventBackend(initial app.ViewState, updates <-chan app.ViewState, submit func(app.Command) error, batchSize int) *EventBackend {
	if batchSize < 1 || batchSize > 256 {
		panic("invalid UI batch size")
	}
	return &EventBackend{current: app.CopyView(initial), updates: updates, submit: submit, batchSize: batchSize}
}

func (b *EventBackend) Snapshot() app.ViewState {
	for i := 0; i < b.batchSize; i++ {
		select {
		case next, open := <-b.updates:
			if !open {
				b.updates = nil
				return app.CopyView(b.current)
			}
			b.current = app.CopyView(next)
		default:
			return app.CopyView(b.current)
		}
	}
	return app.CopyView(b.current)
}

func (b *EventBackend) Submit(c app.Command) error { return b.submit(c) }
