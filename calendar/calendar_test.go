package calendar

import (
	"testing"
	"time"
)

func TestEaster(t *testing.T) {
	for y, want := range map[int]string{2024: "2024-03-31", 2025: "2025-04-20", 2026: "2026-04-05", 2027: "2027-03-28", 2038: "2038-04-25"} {
		if got := Easter(y).String(); got != want {
			t.Errorf("Easter(%d) = %s, want %s", y, got, want)
		}
	}
}

func TestNationalHolidays2026(t *testing.T) {
	var n National
	for _, d := range []string{"2026-01-01", "2026-02-16", "2026-02-17", "2026-04-03", "2026-04-21", "2026-05-01",
		"2026-06-04", "2026-09-07", "2026-10-12", "2026-11-02", "2026-11-15", "2026-11-20", "2026-12-25"} {
		if !n.IsHoliday(MustParse(d)) {
			t.Errorf("%s should be a holiday", d)
		}
	}
	if n.IsHoliday(MustParse("2023-11-20")) {
		t.Error("Nov 20 became national only in 2024")
	}
	if n.IsHoliday(MustParse("2026-02-18")) {
		t.Error("Ash Wednesday is a business day (from noon)")
	}
	local := National{Extra: map[Date]string{MustParse("2026-01-20"): "São Sebastião (Rio de Janeiro)"}}
	if !local.IsHoliday(MustParse("2026-01-20")) || IsBusinessDay(local, MustParse("2026-01-20")) {
		t.Error("local holiday ignored")
	}
}

func TestMonthlyClampsToMonthEnd(t *testing.T) {
	start := MustParse("2026-01-31")
	want := []string{"2026-01-31", "2026-02-28", "2026-03-31", "2026-04-30", "2028-02-29"}
	ks := []int{0, 1, 2, 3, 25}
	for i, k := range ks {
		if got := CycleStart(start, Monthly, k).String(); got != want[i] {
			t.Errorf("cycle %d = %s, want %s", k, got, want[i])
		}
	}
	if got := CycleStart(MustParse("2028-02-29"), Annual, 1).String(); got != "2029-02-28" {
		t.Errorf("annual leap day = %s", got)
	}
	if got := CycleStart(MustParse("2026-11-27"), Weekly, 3).String(); got != "2026-12-18" {
		t.Errorf("weekly = %s", got)
	}
	if got := CycleStart(MustParse("2026-11-30"), Quarterly, 1).String(); got != "2027-02-28" {
		t.Errorf("quarterly = %s", got)
	}
}

func TestDueDateAdjustment(t *testing.T) {
	var n National
	// 2026-11-15 is a Sunday and a holiday: next business day Monday 16.
	if got := DueDate(n, MustParse("2026-10-15"), Monthly, 1, true).String(); got != "2026-11-16" {
		t.Errorf("adjusted = %s", got)
	}
	if got := DueDate(n, MustParse("2026-10-15"), Monthly, 1, false).String(); got != "2026-11-15" {
		t.Errorf("unadjusted = %s", got)
	}
	// Weekly on Fridays: Good Friday 2026-04-03 moves to Monday 04-06,
	// which is still before the next Friday.
	if got := DueDate(n, MustParse("2026-03-27"), Weekly, 1, true).String(); got != "2026-04-06" {
		t.Errorf("good friday = %s", got)
	}
	// Weekly on Saturdays: moving forward lands on Monday, inside the cycle.
	if got := DueDate(n, MustParse("2026-11-28"), Weekly, 0, true).String(); got != "2026-11-30" {
		t.Errorf("saturday = %s", got)
	}
}

func TestDueDateNeverLeavesItsCycle(t *testing.T) {
	// A custom calendar where the whole following week is a holiday forces
	// the backward adjustment.
	h := National{Extra: map[Date]string{}}
	for d := MustParse("2026-12-07"); d.Before(MustParse("2026-12-14")); d = d.AddDays(1) {
		h.Extra[d] = "recesso"
	}
	start := MustParse("2026-11-30") // Monday, weekly
	got := DueDate(h, start, Weekly, 1, true)
	if got.String() != "2026-12-04" {
		t.Fatalf("backward adjustment = %s", got)
	}
}

func TestCycleOf(t *testing.T) {
	start := MustParse("2026-01-31")
	for _, p := range []Periodicity{Weekly, Monthly, Quarterly, Semiannual, Annual} {
		for k := 0; k < 30; k++ {
			d := CycleStart(start, p, k)
			if got := CycleOf(start, p, d); got != k {
				t.Fatalf("%s: CycleOf(cycle %d start %s) = %d", p, k, d, got)
			}
			if got := CycleOf(start, p, CycleStart(start, p, k+1).AddDays(-1)); got != k {
				t.Fatalf("%s: CycleOf(end of cycle %d) = %d", p, k, got)
			}
		}
	}
	if CycleOf(start, Monthly, MustParse("2026-01-01")) != -1 {
		t.Fatal("before start")
	}
}

func TestDateBasics(t *testing.T) {
	d := MustParse("2026-11-27")
	if d.Weekday() != time.Friday || d.AddDays(5).String() != "2026-12-02" || d.DaysUntil(MustParse("2026-12-07")) != 10 {
		t.Fatal("date arithmetic")
	}
	if _, err := Parse("27/11/2026"); err != ErrDate {
		t.Fatal("bad format accepted")
	}
	if d.At(22, 0, BRT).UTC().Hour() != 1 {
		t.Fatal("BRT offset")
	}
}
