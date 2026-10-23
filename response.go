package dtekkrem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// homeNumResponse is the answer to the site's getHomeNum request.
type homeNumResponse struct {
	Result          bool              `json:"result"`
	Text            string            `json:"text"`
	Data            phpMap[houseInfo] `json:"data"`
	UpdateTimestamp string            `json:"updateTimestamp"` // "20:19 23.10.2026"
	Fact            *factInfo         `json:"fact"`
	Preset          *presetInfo       `json:"preset"`
}

type houseInfo struct {
	Type          string     `json:"type"`     // "1" planned works, "2" other outages, "" no outage
	SubType       string     `json:"sub_type"` // outage reason
	StartDate     string     `json:"start_date"`
	EndDate       string     `json:"end_date"`
	SubTypeReason stringList `json:"sub_type_reason"` // schedule groups, e.g. ["GPV2.2"]
}

type factInfo struct {
	// Data maps a day (unix time of its midnight) to schedule groups, and a group to hour slots "1".."24".
	Data   phpMap[phpMap[phpMap[Status]]] `json:"data"`
	Update string                         `json:"update"` // "23.10.2026 18:51"
}

type presetInfo struct {
	GroupNames phpMap[string] `json:"sch_names"`
}

const (
	outageTimeLayout   = "15:04 02.01.2006"
	scheduleTimeLayout = "02.01.2006 15:04"
	// noHouse is the key the site uses for addresses without house numbers.
	noHouse = "-"
)

var kyiv = sync.OnceValues(func() (*time.Location, error) {
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		// tzdata older than 2022b knows only the old spelling.
		loc, err = time.LoadLocation("Europe/Kiev")
	}
	if err != nil {
		return nil, fmt.Errorf("dtekkrem: load Kyiv time zone: %w", err)
	}
	return loc, nil
})

func (r *homeNumResponse) result(addr Address) (*Result, error) {
	if !r.Result {
		return nil, fmt.Errorf("dtekkrem: site returned an error: %q", r.Text)
	}
	house, ok := findHouse(r.Data, addr.House)
	if !ok {
		return nil, fmt.Errorf("%w: %q on %q in %q", ErrUnknownHouse, addr.House, addr.Street, addr.City)
	}
	loc, err := kyiv()
	if err != nil {
		return nil, err
	}

	res := &Result{Address: addr}
	res.UpdatedAt, _ = time.ParseInLocation(outageTimeLayout, r.UpdateTimestamp, loc)
	if house.Type != "" || house.SubType != "" {
		res.Outage = &Outage{
			Planned: house.Type == "1",
			Reason:  house.SubType,
		}
		res.Outage.Start, _ = time.ParseInLocation(outageTimeLayout, house.StartDate, loc)
		res.Outage.End, _ = time.ParseInLocation(outageTimeLayout, house.EndDate, loc)
	}

	var fact factInfo
	if r.Fact != nil {
		fact = *r.Fact
		res.ScheduleUpdatedAt, _ = time.ParseInLocation(scheduleTimeLayout, fact.Update, loc)
	}
	var names phpMap[string]
	if r.Preset != nil {
		names = r.Preset.GroupNames
	}
	days := make([]string, 0, len(fact.Data))
	for day := range fact.Data {
		days = append(days, day)
	}
	slices.Sort(days) // unix timestamps of the same length sort chronologically

	for _, id := range house.SubTypeReason {
		group := Group{ID: id, Name: names[id]}
		if group.Name == "" {
			group.Name = id
		}
		for _, day := range days {
			slots, ok := fact.Data[day][id]
			if !ok {
				continue
			}
			unix, err := strconv.ParseInt(day, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("dtekkrem: unexpected schedule day %q", day)
			}
			d := Day{Date: time.Unix(unix, 0).In(loc)}
			for slot, status := range slots {
				if h, err := strconv.Atoi(slot); err == nil && h >= 1 && h <= 24 {
					d.Hours[h-1] = status
				}
			}
			group.Days = append(group.Days, d)
		}
		res.Groups = append(res.Groups, group)
	}
	return res, nil
}

// findHouse matches the house number ignoring case and spaces ("12 а" finds "12А").
// An empty number means the street's entry without a house number.
func findHouse(houses phpMap[houseInfo], house string) (houseInfo, bool) {
	if strings.TrimSpace(house) == "" {
		house = noHouse
	}
	if info, ok := houses[house]; ok {
		return info, true
	}
	if info, ok := houses[noHouse]; ok && len(houses) == 1 {
		return info, true
	}
	normalize := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), "")) }
	want := normalize(house)
	for k, info := range houses {
		if normalize(k) == want {
			return info, true
		}
	}
	return houseInfo{}, false
}

// phpMap decodes a JSON object, also accepting [] that PHP produces for an empty array.
type phpMap[V any] map[string]V

func (m *phpMap[V]) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("[]")) {
		*m = nil
		return nil
	}
	return json.Unmarshal(b, (*map[string]V)(m))
}

// stringList decodes a JSON array of strings, also accepting a single string or null.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*l = nil
		if s != "" {
			*l = stringList{s}
		}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(l))
}
