package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
func cfg() Config {
	c := DefaultConfig()
	c.ID = "schedule"
	c.Revision = "r1"
	c.Enabled = true
	c.Timezone = "America/New_York"
	return c
}
func TestScheduleGapFoldAndMonthEnd(t *testing.T) {
	for _, tt := range []struct {
		name, after, clock, frequency string
		day                           int
		want, local                   string
	}{
		{"spring_gap", "2025-03-08T08:00:00Z", "02:30", "daily", 0, "2025-03-09T07:00:00Z", "2025-03-09T02:30"},
		{"fall_fold_earlier", "2025-11-01T08:00:00Z", "01:30", "daily", 0, "2025-11-02T05:30:00Z", "2025-11-02T01:30"},
		{"fold_not_twice", "2025-11-02T05:30:00Z", "01:30", "daily", 0, "2025-11-03T06:30:00Z", "2025-11-03T01:30"},
		{"january_31", "2025-01-30T08:00:00Z", "02:00", "monthly", 31, "2025-01-31T07:00:00Z", "2025-01-31T02:00"},
		{"february_28", "2025-01-31T07:00:00Z", "02:00", "monthly", 31, "2025-02-28T07:00:00Z", "2025-02-28T02:00"},
		{"leap_february_29", "2024-01-31T07:00:00Z", "02:00", "monthly", 31, "2024-02-29T07:00:00Z", "2024-02-29T02:00"},
		{"weekly_sunday_gap", "2025-03-08T08:00:00Z", "02:30", "weekly", 7, "2025-03-09T07:00:00Z", "2025-03-09T02:30"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := cfg()
			c.LocalTime = tt.clock
			c.Frequency = tt.frequency
			if tt.frequency == "monthly" {
				c.Monthday = tt.day
			}
			if tt.frequency == "weekly" {
				c.Weekday = tt.day
			}
			o, err := NextOccurrence(c, utc(tt.after))
			if err != nil {
				t.Fatal(err)
			}
			if !o.Due.Equal(utc(tt.want)) || o.IntendedLocal != tt.local {
				t.Fatalf("got %+v, want %s / %s", o, tt.want, tt.local)
			}
			again, _ := NextOccurrence(c, utc(tt.after))
			if o.ID == "" || o != again {
				t.Fatal("identity not stable")
			}
		})
	}
	// A 30-minute Australian gap resolves to 02:30, not 03:00/03:15.
	c := cfg()
	c.Timezone = "Australia/Lord_Howe"
	c.LocalTime = "02:15"
	o, err := NextOccurrence(c, utc("2025-10-04T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Due.Equal(utc("2025-10-04T15:30:00Z")) {
		t.Fatal(o)
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type admission struct {
	busy     bool
	requests []Request
	claimed  map[string]bool
	fail     bool
}

func (a *admission) ClaimAndAdmit(ctx context.Context, r Request) (AdmissionResult, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if a.fail {
		return "", errors.New("transaction failed")
	}
	if a.busy {
		return Busy, nil
	}
	if a.claimed == nil {
		a.claimed = map[string]bool{}
	}
	if a.claimed[r.Occurrence.ID] {
		return AlreadyClaimed, nil
	}
	a.claimed[r.Occurrence.ID] = true
	a.requests = append(a.requests, r)
	return Admitted, nil
}
func planner(t *testing.T, c Config, clock *fakeClock, a *admission, s State) *Planner {
	t.Helper()
	p, err := NewPlanner(c, clock, a, s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestScheduleClockJumpAndRevision(t *testing.T) {
	ctx := context.Background()
	c := cfg()
	clock := &fakeClock{utc("2025-03-08T06:59:00Z")}
	a := &admission{}
	p := planner(t, c, clock, a, State{})
	clock.now = utc("2025-03-08T07:00:00Z")
	before := p.State()
	r, err := p.Tick(ctx, false)
	if err != nil || r.Status != "admitted" {
		t.Fatalf("%+v %v", r, err)
	}
	if len(a.requests) != 1 || !a.requests[0].Unattended || a.requests[0].CatchUp {
		t.Fatal(a.requests)
	}
	// Simulate crash after atomic admission but before saving planner state.
	p = planner(t, c, clock, a, before)
	r, err = p.Tick(ctx, false)
	if err != nil || r.Status != "already_claimed" || len(a.requests) != 1 {
		t.Fatalf("duplicate admission %+v %v", r, err)
	}
	clock.now = utc("2025-03-07T07:00:00Z")
	r, _ = p.Tick(ctx, false)
	if r.Status != "waiting" || len(a.requests) != 1 {
		t.Fatal("backward clock replayed")
	}
	clock.now = utc("2025-06-01T10:00:00Z")
	r, _ = p.Tick(ctx, true)
	if r.Status != "missed" || r.Offer == nil || len(a.requests) != 1 {
		t.Fatal("clock jump auto-ran")
	}
	oldOffer := r.Offer.ID
	c.Revision = "r2"
	c.Timezone = "UTC"
	if err := p.Replace(c); err != nil {
		t.Fatal(err)
	}
	if p.State().Offer != nil || !p.State().Next.Due.Equal(utc("2025-06-02T02:00:00Z")) {
		t.Fatal(p.State())
	}
	if _, err := p.Respond(ctx, oldOffer, true); err == nil {
		t.Fatal("accepted stale offer")
	}
	modified := c
	modified.LocalTime = "03:00"
	if err := p.Replace(modified); err == nil {
		t.Fatal("edit without revision accepted")
	}
	o1, _ := NextOccurrence(cfg(), utc("2025-03-08T06:00:00Z"))
	c2 := cfg()
	c2.Revision = "other"
	o2, _ := NextOccurrence(c2, utc("2025-03-08T06:00:00Z"))
	if o1.ID == o2.ID {
		t.Fatal("revision not in identity")
	}
}
func TestCatchUpOfferOnceRequiresAcceptance(t *testing.T) {
	ctx := context.Background()
	c := cfg()
	clock := &fakeClock{utc("2025-01-01T06:00:00Z")}
	a := &admission{}
	p := planner(t, c, clock, a, State{})
	clock.now = utc("2025-01-10T12:00:00Z")
	r, err := p.Tick(ctx, true)
	if err != nil || r.Offer == nil || len(a.requests) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	id := r.Offer.ID
	if r.Offer.First.IntendedLocal != "2025-01-01T02:00" || r.Offer.Last.IntendedLocal != "2025-01-10T02:00" {
		t.Fatal(r.Offer)
	}
	p = planner(t, c, clock, a, p.State())
	for i := 0; i < 3; i++ {
		r, _ = p.Tick(ctx, true)
		if r.Offer == nil || r.Offer.ID != id {
			t.Fatal("offer duplicated/lost")
		}
	}
	a.busy = true
	result, err := p.Respond(ctx, id, true)
	if err != nil || result != Busy || p.State().Offer == nil || len(a.requests) != 0 {
		t.Fatal("busy queued a run")
	}
	clock.now = utc("2025-01-12T12:00:00Z")
	r, _ = p.Tick(ctx, true)
	if r.Offer.ID != id || r.Offer.Last.IntendedLocal != "2025-01-12T02:00" {
		t.Fatal(r.Offer)
	}
	a.busy = false
	result, err = p.Respond(ctx, id, true)
	if err != nil || result != Admitted || p.State().Offer != nil || len(a.requests) != 1 {
		t.Fatal(result, err)
	}
	if a.requests[0].Unattended || !a.requests[0].CatchUp || a.requests[0].Occurrence.IntendedLocal != "2025-01-12T02:00" {
		t.Fatal(a.requests)
	}
	if _, err := p.Respond(ctx, id, true); err == nil {
		t.Fatal("offer accepted twice")
	}
	clock.now = utc("2025-01-14T12:00:00Z")
	r, _ = p.Tick(ctx, true)
	if _, err := p.Respond(ctx, r.Offer.ID, false); err != nil {
		t.Fatal(err)
	}
	r, _ = p.Tick(ctx, true)
	if r.Offer != nil || len(a.requests) != 1 {
		t.Fatal("dismissal replayed")
	}
	// Ordinary due while busy becomes missed, with no queue or auto retry.
	clock.now = p.State().Next.Due
	a.busy = true
	r, _ = p.Tick(ctx, false)
	if r.Status != "missed" || r.Offer == nil {
		t.Fatal(r)
	}
	a.busy = false
	r, _ = p.Tick(ctx, false)
	if len(a.requests) != 1 || r.Offer == nil {
		t.Fatal("busy occurrence auto-retried")
	}
}
func TestScheduleValidationAndTransactionFailure(t *testing.T) {
	for _, edit := range []func(*Config){func(c *Config) { c.Timezone = "Local" }, func(c *Config) { c.LocalTime = "2:00" }, func(c *Config) { c.Monthday = 1 }, func(c *Config) { c.CatchUp = "automatic" }, func(c *Config) { c.Frequency = "weekly"; c.Weekday = 8 }, func(c *Config) { c.Frequency = "monthly"; c.Monthday = 32 }} {
		c := cfg()
		edit(&c)
		if c.Validate() == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := cfg()
	clock := &fakeClock{utc("2025-01-01T06:00:00Z")}
	a := &admission{fail: true}
	p := planner(t, c, clock, a, State{})
	before := p.State()
	clock.now = before.Next.Due
	if _, err := p.Tick(context.Background(), false); err == nil {
		t.Fatal("expected transaction error")
	}
	if p.State().Next != before.Next {
		t.Fatal("failed transaction consumed occurrence")
	}
	c.Enabled = false
	c.Revision = "disabled"
	if err := p.Replace(c); err != nil {
		t.Fatal(err)
	}
	r, err := p.Tick(context.Background(), true)
	if err != nil || r.Status != "disabled" {
		t.Fatal(r, err)
	}
}
