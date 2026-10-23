package dtekkrem

import (
	"time"
)

// Address of a house as spelled on the site, e.g. {"м. Бориспіль", "вул. Київський Шлях", "6"}.
// House may be empty for addresses the site lists without house numbers.
type Address struct {
	City   string
	Street string
	House  string
}

type Result struct {
	Address Address
	// Outage is the outage currently reported for the address, nil when there is none.
	Outage *Outage
	// Groups are the outage schedule groups (черги) the house belongs to. A house fed by
	// several lines has several groups, and its apartments may follow different schedules.
	Groups []Group
	// UpdatedAt is when the site last refreshed the current outage information.
	UpdatedAt time.Time
	// ScheduleUpdatedAt is when the group schedules were last published.
	ScheduleUpdatedAt time.Time
}

type Outage struct {
	// Planned is true for planned repair works; otherwise Reason tells the kind of outage,
	// e.g. "Стабілізаційне відключення (Згідно графіку погодинних відключень)".
	Planned bool
	Reason  string
	Start   time.Time
	// End is the expected restoration time.
	End time.Time
}

const emergencyReason = "Екстренні відключення (Аварійне без застосування графіку погодинних відключень)"

// IsEmergency reports an emergency outage, during which the group schedules do not apply.
func (o *Outage) IsEmergency() bool {
	return o.Reason == emergencyReason
}

type Group struct {
	ID   string // e.g. "GPV2.2"
	Name string // e.g. "Черга 2.2"
	// Days holds the published schedules, today first; tomorrow's appears once DTEK publishes it.
	Days []Day
}

type Day struct {
	Date time.Time // midnight in Europe/Kyiv
	// Hours[h] is the status of the hour starting at h:00.
	Hours [24]Status
}

type Status string

const (
	StatusOn              Status = "yes"
	StatusOff             Status = "no"
	StatusMaybe           Status = "maybe"
	StatusOffFirstHalf    Status = "first"   // off for the first 30 minutes of the hour
	StatusOffSecondHalf   Status = "second"  // off for the second 30 minutes of the hour
	StatusMaybeFirstHalf  Status = "mfirst"  // possibly off for the first 30 minutes
	StatusMaybeSecondHalf Status = "msecond" // possibly off for the second 30 minutes
)

type halfHour int

const (
	powerOn halfHour = iota
	powerOff
	powerMaybeOff
)

func (s Status) halves() [2]halfHour {
	switch s {
	case StatusOff:
		return [2]halfHour{powerOff, powerOff}
	case StatusMaybe:
		return [2]halfHour{powerMaybeOff, powerMaybeOff}
	case StatusOffFirstHalf:
		return [2]halfHour{powerOff, powerOn}
	case StatusOffSecondHalf:
		return [2]halfHour{powerOn, powerOff}
	case StatusMaybeFirstHalf:
		return [2]halfHour{powerMaybeOff, powerOn}
	case StatusMaybeSecondHalf:
		return [2]halfHour{powerOn, powerMaybeOff}
	}
	return [2]halfHour{powerOn, powerOn}
}

// Interval is a period without power.
type Interval struct {
	Start time.Time
	End   time.Time
	// Maybe marks a possible outage rather than a scheduled one.
	Maybe bool
}

// Outages merges the group's schedule into outage intervals, joining those that continue past midnight.
func (g Group) Outages() []Interval {
	var res []Interval
	for _, day := range g.Days {
		y, m, d := day.Date.Date()
		for h, status := range day.Hours {
			for half, state := range status.halves() {
				if state == powerOn {
					continue
				}
				minute := h*60 + half*30
				start := time.Date(y, m, d, 0, minute, 0, 0, day.Date.Location())
				end := time.Date(y, m, d, 0, minute+30, 0, 0, day.Date.Location())
				maybe := state == powerMaybeOff
				if n := len(res); n > 0 && res[n-1].End.Equal(start) && res[n-1].Maybe == maybe {
					res[n-1].End = end
					continue
				}
				res = append(res, Interval{Start: start, End: end, Maybe: maybe})
			}
		}
	}
	return res
}
