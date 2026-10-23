package dtekkrem

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Trimmed getHomeNum response; day keys are 23.10.2026 and 24.10.2026 00:00 in Kyiv.
const sampleResponse = `{
	"result": true,
	"data": {
		"6": {"sub_type": "Стабілізаційне відключення (Згідно графіку погодинних відключень)", "start_date": "17:56 23.10.2026",
			"end_date": "22:00 23.10.2026", "type": "2", "sub_type_reason": ["GPV2.2"], "voluntarily": null, "cek": null},
		"12А": {"sub_type": "", "start_date": "", "end_date": "", "type": "", "sub_type_reason": ["GPV5.2", "GPV3.2"], "voluntarily": null, "cek": null},
		"14": {"sub_type": "", "start_date": "", "end_date": "", "type": "", "sub_type_reason": "", "voluntarily": null, "cek": null}
	},
	"updateTimestamp": "20:19 23.10.2026",
	"fact": {
		"data": {
			"1792789200": {"GPV2.2": {"1": "first", "2": "yes", "3": "maybe", "4": "msecond", "5": "no"}},
			"1792702800": {"GPV2.2": {"1": "yes", "9": "no", "10": "no", "11": "second", "12": "no", "23": "no", "24": "no"}, "GPV5.2": {"1": "yes"}}
		},
		"update": "23.10.2026 18:51",
		"today": 1792702800
	},
	"preset": {"sch_names": {"GPV2.2": "Черга 2.2", "GPV5.2": "Черга 5.2"}, "time_zone": [], "data": []}
}`

func parseSample(t *testing.T, body string, addr Address) (*Result, error) {
	t.Helper()
	var resp homeNumResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.result(addr)
}

func kyivTime(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := kyiv()
	if err != nil {
		t.Fatal(err)
	}
	tm, err := time.ParseInLocation("02.01.2006 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestResultWithOutage(t *testing.T) {
	res, err := parseSample(t, sampleResponse, Address{City: "м. Бориспіль", Street: "вул. Київський Шлях", House: "6"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outage == nil || res.Outage.Planned || res.Outage.IsEmergency() {
		t.Fatalf("unexpected outage: %+v", res.Outage)
	}
	if !res.Outage.Start.Equal(kyivTime(t, "23.10.2026 17:56")) || !res.Outage.End.Equal(kyivTime(t, "23.10.2026 22:00")) {
		t.Errorf("outage period = %v – %v", res.Outage.Start, res.Outage.End)
	}
	if !res.UpdatedAt.Equal(kyivTime(t, "23.10.2026 20:19")) || !res.ScheduleUpdatedAt.Equal(kyivTime(t, "23.10.2026 18:51")) {
		t.Errorf("updated at %v, schedule updated at %v", res.UpdatedAt, res.ScheduleUpdatedAt)
	}
	if len(res.Groups) != 1 || res.Groups[0].ID != "GPV2.2" || res.Groups[0].Name != "Черга 2.2" {
		t.Fatalf("groups = %+v", res.Groups)
	}
	days := res.Groups[0].Days
	if len(days) != 2 || !days[0].Date.Equal(kyivTime(t, "23.10.2026 00:00")) || !days[1].Date.Equal(kyivTime(t, "24.10.2026 00:00")) {
		t.Fatalf("days are not today and tomorrow in order: %+v", days)
	}
	if days[0].Hours[8] != StatusOff || days[0].Hours[10] != StatusOffSecondHalf || days[1].Hours[0] != StatusOffFirstHalf {
		t.Errorf("hours mapped wrong: %v / %v", days[0].Hours, days[1].Hours)
	}

	want := []Interval{
		{Start: kyivTime(t, "23.10.2026 08:00"), End: kyivTime(t, "23.10.2026 10:00")},
		{Start: kyivTime(t, "23.10.2026 10:30"), End: kyivTime(t, "23.10.2026 12:00")},
		{Start: kyivTime(t, "23.10.2026 22:00"), End: kyivTime(t, "24.10.2026 00:30")},
		{Start: kyivTime(t, "24.10.2026 02:00"), End: kyivTime(t, "24.10.2026 03:00"), Maybe: true},
		{Start: kyivTime(t, "24.10.2026 03:30"), End: kyivTime(t, "24.10.2026 04:00"), Maybe: true},
		{Start: kyivTime(t, "24.10.2026 04:00"), End: kyivTime(t, "24.10.2026 05:00")},
	}
	got := res.Groups[0].Outages()
	if len(got) != len(want) {
		t.Fatalf("outages = %+v", got)
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) || got[i].Maybe != want[i].Maybe {
			t.Errorf("outage %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestResultHouseMatching(t *testing.T) {
	res, err := parseSample(t, sampleResponse, Address{House: "12 а"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outage != nil {
		t.Errorf("outage = %+v, want none", res.Outage)
	}
	// GPV5.2 has a schedule for today only, GPV3.2 has none.
	if len(res.Groups) != 2 || len(res.Groups[0].Days) != 1 || res.Groups[1].Name != "GPV3.2" || res.Groups[1].Days != nil {
		t.Errorf("groups = %+v", res.Groups)
	}

	res, err = parseSample(t, sampleResponse, Address{House: "14"})
	if err != nil || len(res.Groups) != 0 {
		t.Errorf("house without groups: %+v, %v", res, err)
	}

	if _, err := parseSample(t, sampleResponse, Address{House: "7"}); !errors.Is(err, ErrUnknownHouse) {
		t.Errorf("err = %v, want ErrUnknownHouse", err)
	}
}

func TestResultWithoutHouseNumbers(t *testing.T) {
	body := `{"result": true, "data": {"-": {"sub_type": "", "start_date": "", "end_date": "", "type": "", "sub_type_reason": ["GPV3.1"]}},
		"updateTimestamp": "20:19 23.10.2026", "fact": {"data": [], "update": "23.10.2026 18:51", "today": 1792702800}, "preset": {"sch_names": []}}`
	res, err := parseSample(t, body, Address{City: "Бориспільська ТГ", Street: "Без вулиці"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Name != "GPV3.1" || res.Groups[0].Days != nil {
		t.Errorf("groups = %+v", res.Groups)
	}
	// The only entry is used even when a house number is given.
	if _, err := parseSample(t, body, Address{House: "5"}); err != nil {
		t.Error(err)
	}

	body = `{"result": true, "data": {"8": {"sub_type_reason": ["GPV3.1"]}, "-": {"sub_type_reason": ["GPV2.2"]}}}`
	res, err = parseSample(t, body, Address{House: " "})
	if err != nil || len(res.Groups) != 1 || res.Groups[0].ID != "GPV2.2" {
		t.Errorf("empty house among numbered ones: %+v, %v", res, err)
	}
}

func TestResultError(t *testing.T) {
	if _, err := parseSample(t, `{"result": false, "text": "Error"}`, Address{}); err == nil {
		t.Error("expected an error")
	}
}
