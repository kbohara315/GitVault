package app

// Events carries full immutable operational snapshots. Progress may be
// coalesced, but publication never waits for the UI. Durable results live in
// SQLite and do not rely on delivery through this channel.
type Events struct{ updates chan ViewState }

func NewEvents(capacity int) *Events {
	if capacity < 1 || capacity > 4096 {
		panic("invalid UI event capacity")
	}
	return &Events{updates: make(chan ViewState, capacity)}
}

func (e *Events) Updates() <-chan ViewState { return e.updates }

// Publish has one owner: the coordinator worker. Consumers receive copies.
func (e *Events) Publish(state ViewState) {
	state = CopyView(state)
	select {
	case e.updates <- state:
		return
	default:
	}
	select {
	case <-e.updates:
	default:
	}
	select {
	case e.updates <- state:
	default:
	}
}

func CopyView(s ViewState) ViewState {
	s.Repositories = append([]RepositoryView(nil), s.Repositories...)
	s.Snapshots = append([]SnapshotView(nil), s.Snapshots...)
	s.Remote = append([]SnapshotView(nil), s.Remote...)
	s.Tasks = append([]TaskView(nil), s.Tasks...)
	s.Logs = append([]LogEntry(nil), s.Logs...)
	return s
}
