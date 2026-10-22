// Package scheduler interprets local calendar schedules without timers, secret
// access, or engine calls. A caller owns serialization and persistence of State.
package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	GapFirstValid    = "first_valid_instant"
	FoldEarlierOnce  = "earlier_once"
	MonthLastValid   = "last_valid_day"
	CatchUpOfferOnce = "offer_once"
)

type Config struct {
	ID, Revision                        string
	Enabled                             bool
	Timezone, LocalTime, Frequency      string
	Weekday, Monthday                   int // Monday=1; zero means absent.
	CatchUp, Gap, Fold, InvalidMonthday string
}

func DefaultConfig() Config {
	return Config{LocalTime: "02:00", Frequency: "daily", CatchUp: CatchUpOfferOnce,
		Gap: GapFirstValid, Fold: FoldEarlierOnce, InvalidMonthday: MonthLastValid}
}

func (c Config) Validate() error {
	if c.ID == "" || c.Revision == "" {
		return errors.New("schedule ID and revision required")
	}
	if len(c.LocalTime) != 5 || c.LocalTime[2] != ':' {
		return errors.New("local time must be HH:MM")
	}
	t, err := time.Parse("15:04", c.LocalTime)
	if err != nil || t.Format("15:04") != c.LocalTime {
		return errors.New("invalid local time")
	}
	if c.CatchUp != CatchUpOfferOnce || c.Gap != GapFirstValid || c.Fold != FoldEarlierOnce || c.InvalidMonthday != MonthLastValid {
		return errors.New("unsupported schedule semantics")
	}
	switch c.Frequency {
	case "daily":
		if c.Weekday != 0 || c.Monthday != 0 {
			return errors.New("daily schedule has day fields")
		}
	case "weekly":
		if c.Weekday < 1 || c.Weekday > 7 || c.Monthday != 0 {
			return errors.New("invalid weekly schedule")
		}
	case "monthly":
		if c.Monthday < 1 || c.Monthday > 31 || c.Weekday != 0 {
			return errors.New("invalid monthly schedule")
		}
	default:
		return errors.New("invalid frequency")
	}
	if c.Timezone == "" && !c.Enabled {
		return nil
	}
	if c.Timezone == "" || c.Timezone == "Local" || len(c.Timezone) > 128 || strings.Contains(c.Timezone, "\\") {
		return errors.New("explicit IANA timezone required")
	}
	_, err = time.LoadLocation(c.Timezone)
	return err
}

type Occurrence struct {
	ID, ScheduleID, Revision, IntendedLocal string
	Due                                     time.Time
}

func occurrence(c Config, date time.Time, due time.Time) Occurrence {
	o := Occurrence{ScheduleID: c.ID, Revision: c.Revision, IntendedLocal: date.Format("2006-01-02") + "T" + c.LocalTime, Due: due.UTC()}
	b, _ := json.Marshal(o)
	h := sha256.Sum256(b)
	o.ID = hex.EncodeToString(h[:])
	return o
}

// NextOccurrence is strictly after after. Calendar arithmetic is independent of
// time.Date's unspecified choice of side in a daylight-saving gap or fold.
func NextOccurrence(c Config, after time.Time) (Occurrence, error) {
	if err := c.Validate(); err != nil {
		return Occurrence{}, err
	}
	if !c.Enabled {
		return Occurrence{}, errors.New("schedule disabled")
	}
	loc, _ := time.LoadLocation(c.Timezone)
	local := after.In(loc)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	clock, _ := time.Parse("15:04", c.LocalTime)
	for i := 0; i < 370; i++ {
		match := c.Frequency == "daily"
		if c.Frequency == "weekly" {
			match = (int(date.Weekday())+6)%7+1 == c.Weekday
		}
		if c.Frequency == "monthly" {
			last := time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
			day := c.Monthday
			if day > last {
				day = last
			}
			match = date.Day() == day
		}
		if match {
			wall := date.Add(time.Duration(clock.Hour())*time.Hour + time.Duration(clock.Minute())*time.Minute)
			due := resolve(wall, loc)
			if due.After(after) {
				return occurrence(c, date, due), nil
			}
		}
		date = date.AddDate(0, 0, 1)
	}
	return Occurrence{}, errors.New("no occurrence in bounded calendar search")
}

// Walk constant-offset intervals. This also handles non-hour transitions and
// historical second offsets: gap resolution is the transition instant itself.
func resolve(wall time.Time, loc *time.Location) time.Time {
	start, limit := wall.Add(-48*time.Hour), wall.Add(48*time.Hour)
	var exact, gap, gapWall time.Time
	for start.Before(limit) {
		_, off := start.In(loc).Zone()
		_, end := start.In(loc).ZoneBounds()
		if end.IsZero() || end.After(limit) {
			end = limit
		}
		candidate := wall.Add(-time.Duration(off) * time.Second)
		if !candidate.Before(start) && candidate.Before(end) && (exact.IsZero() || candidate.Before(exact)) {
			exact = candidate
		}
		localStart := start.Add(time.Duration(off) * time.Second)
		if !localStart.Before(wall) && (gap.IsZero() || localStart.Before(gapWall)) {
			gap, gapWall = start, localStart
		}
		start = end
	}
	if !exact.IsZero() {
		return exact.UTC()
	}
	return gap.UTC()
}

type Clock interface{ Now() time.Time }
type AdmissionResult string

const (
	Admitted            AdmissionResult = "admitted"
	AlreadyClaimed      AdmissionResult = "already_claimed"
	Busy                AdmissionResult = "busy"
	InteractionRequired AdmissionResult = "interaction_required"
)

type Request struct {
	Occurrence Occurrence
	CatchUp    bool
	Unattended bool
}

// Admission MUST claim occurrence ID and admit the run in ONE transaction.
// Busy/interaction-required must not claim it. Unattended must not prompt.
// This module deliberately provides no pretend production claim persistence.
type Admission interface {
	ClaimAndAdmit(context.Context, Request) (AdmissionResult, error)
}

type Offer struct {
	ID          string
	First, Last Occurrence
}
type State struct {
	ScheduleID   string
	Revision     string
	Next         Occurrence
	LastObserved time.Time
	Offer        *Offer
}
type TickResult struct {
	Status     string
	Occurrence Occurrence
	Offer      *Offer
}
type Planner struct {
	config    Config
	clock     Clock
	admission Admission
	state     State
}

// NewPlanner resumes caller-persisted state. A fresh planner starts in the
// future; resume/wake callers use Tick(reconcile=true) to offer missed work.
func NewPlanner(c Config, clock Clock, admission Admission, saved State) (*Planner, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if clock == nil || admission == nil {
		return nil, errors.New("clock and admission required")
	}
	p := &Planner{config: c, clock: clock, admission: admission}
	if saved.ScheduleID == c.ID && saved.Revision == c.Revision {
		p.state = cloneState(saved)
		if c.Enabled && saved.Next.ID != "" {
			// Re-resolve the intended date against current timezone data on
			// every startup, without rewinding an already consumed cursor.
			d, err := time.Parse("2006-01-02T15:04", saved.Next.IntendedLocal)
			if err != nil || saved.Next.ScheduleID != c.ID || saved.Next.Revision != c.Revision || d.Format("15:04") != c.LocalTime {
				return nil, errors.New("saved occurrence does not match schedule")
			}
			loc, _ := time.LoadLocation(c.Timezone)
			p.state.Next = occurrence(c, d, resolve(d, loc))
		}
	}
	if p.state.Revision != c.Revision || (p.state.Next.ID == "" && c.Enabled) {
		if err := p.reset(c); err != nil {
			return nil, err
		}
	}
	return p, nil
}
func cloneState(s State) State {
	if s.Offer != nil {
		o := *s.Offer
		s.Offer = &o
	}
	return s
}
func (p *Planner) State() State { return cloneState(p.state) }
func (p *Planner) reset(c Config) error {
	s := State{ScheduleID: c.ID, Revision: c.Revision, LastObserved: p.clock.Now().UTC()}
	if c.Enabled {
		var err error
		s.Next, err = NextOccurrence(c, s.LastObserved)
		if err != nil {
			return err
		}
	}
	p.config, p.state = c, s
	return nil
}

// Replace invalidates old offers; a semantic edit MUST have a new revision.
func (p *Planner) Replace(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Revision == p.config.Revision && c != p.config {
		return errors.New("schedule edit requires new revision")
	}
	if c == p.config {
		return nil
	}
	return p.reset(c)
}
func (p *Planner) Tick(ctx context.Context, reconcile bool) (TickResult, error) {
	if err := ctx.Err(); err != nil {
		return TickResult{}, err
	}
	if !p.config.Enabled {
		return TickResult{Status: "disabled"}, nil
	}
	now := p.clock.Now().UTC()
	// A backward clock never rewinds the occurrence cursor.
	p.state.LastObserved = now
	due := p.state.Next
	if now.Before(due.Due) {
		return TickResult{Status: "waiting", Offer: p.State().Offer}, nil
	}
	next, err := NextOccurrence(p.config, now)
	if err != nil {
		return TickResult{}, err
	}
	missed := reconcile || now.Sub(due.Due) >= time.Minute || p.state.Offer != nil
	if !missed {
		r, err := p.admission.ClaimAndAdmit(ctx, Request{Occurrence: due, Unattended: true})
		if err != nil {
			return TickResult{}, err
		}
		if r == Admitted || r == AlreadyClaimed {
			p.state.Next = next
			return TickResult{Status: string(r), Occurrence: due}, nil
		}
		if r != Busy && r != InteractionRequired {
			return TickResult{}, fmt.Errorf("unknown admission result %q", r)
		}
	}
	// Find the latest missed calendar occurrence without replaying a backlog.
	last := due
	if prev, err := latest(p.config, now); err == nil && !prev.Due.Before(due.Due) {
		last = prev
	}
	if p.state.Offer == nil {
		p.state.Offer = &Offer{ID: due.ID, First: due, Last: last}
	} else {
		p.state.Offer.Last = last
	}
	p.state.Next = next
	return TickResult{Status: "missed", Occurrence: due, Offer: p.State().Offer}, nil
}
func latest(c Config, now time.Time) (Occurrence, error) {
	// At most one candidate per local date; bounded regardless of downtime.
	loc, _ := time.LoadLocation(c.Timezone)
	d := now.In(loc)
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -35)
	o, err := NextOccurrence(c, start)
	var last Occurrence
	for i := 0; err == nil && i < 38 && !o.Due.After(now); i++ {
		last = o
		o, err = NextOccurrence(c, o.Due)
	}
	if last.ID == "" {
		return last, errors.New("no previous occurrence")
	}
	return last, nil
}

// Respond consumes dismissal once. Busy acceptance preserves the single offer;
// acceptance uses the latest missed occurrence, never one run per missed date.
func (p *Planner) Respond(ctx context.Context, offerID string, accept bool) (AdmissionResult, error) {
	if p.state.Offer == nil || p.state.Offer.ID != offerID {
		return "", errors.New("stale catch-up offer")
	}
	if !accept {
		p.state.Offer = nil
		return "dismissed", nil
	}
	r, err := p.admission.ClaimAndAdmit(ctx, Request{Occurrence: p.state.Offer.Last, CatchUp: true, Unattended: false})
	if err != nil {
		return "", err
	}
	if r == Admitted || r == AlreadyClaimed {
		p.state.Offer = nil
	}
	return r, nil
}
