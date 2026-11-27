// Package calendar provides civil dates, Brazilian national banking holidays
// and the cycle arithmetic of Pix Automático: weekly, monthly, quarterly,
// semiannual and annual recurrences anchored on a start date, with optional
// adjustment to the next business day that never crosses into the next
// cycle.
package calendar

import (
	"errors"
	"fmt"
	"time"
)

// Date is a civil date with no time zone.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// ErrDate reports an unparseable date.
var ErrDate = errors.New("calendar: invalid date, want YYYY-MM-DD")

// D builds a date, normalizing overflow (D(2026, 2, 31) is 2026-03-03).
func D(y int, m time.Month, d int) Date { return FromTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) }

// FromTime returns the civil date of t in t's location.
func FromTime(t time.Time) Date {
	y, m, d := t.Date()
	return Date{y, m, d}
}

// Parse parses YYYY-MM-DD.
func Parse(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return Date{}, ErrDate
	}
	return FromTime(t), nil
}

// MustParse is Parse for constants; it panics on error.
func MustParse(s string) Date {
	d, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

func (d Date) t() time.Time { return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC) }

// String formats YYYY-MM-DD.
func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day) }

// IsZero reports whether d is the zero date.
func (d Date) IsZero() bool { return d == Date{} }

// AddDays returns d plus n days.
func (d Date) AddDays(n int) Date { return FromTime(d.t().AddDate(0, 0, n)) }

// Weekday returns the day of the week.
func (d Date) Weekday() time.Weekday { return d.t().Weekday() }

// Before reports d < o.
func (d Date) Before(o Date) bool { return d.t().Before(o.t()) }

// After reports d > o.
func (d Date) After(o Date) bool { return d.t().After(o.t()) }

// DaysUntil returns the number of days from d to o (negative if o is earlier).
func (d Date) DaysUntil(o Date) int { return int(o.t().Sub(d.t()).Hours() / 24) }

// At returns the instant at hh:mm on d in loc.
func (d Date) At(hh, mm int, loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, hh, mm, 0, 0, loc)
}

// BRT is Brasília time. Brazil has had no daylight saving time since 2019,
// so a fixed offset avoids depending on the tz database.
var BRT = time.FixedZone("BRT", -3*60*60)

// addMonths adds n months keeping the anchor day, clamped to the month's
// last day (an anchor of 31 falls on Feb 28/29, Apr 30, ...).
func addMonths(anchor Date, n int) Date {
	first := time.Date(anchor.Year, anchor.Month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
	last := first.AddDate(0, 1, -1).Day()
	day := anchor.Day
	if day > last {
		day = last
	}
	return Date{first.Year(), first.Month(), day}
}

// Easter returns Easter Sunday (Gregorian, anonymous algorithm).
func Easter(year int) Date {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return Date{year, time.Month(month), day}
}

// Holidays is a set of non-business dates.
type Holidays interface {
	IsHoliday(Date) bool
}

// National is the Brazilian national banking calendar: national holidays
// (including Black Consciousness Day, national since 2024) plus Carnival
// Monday and Tuesday, when the financial system does not settle.
type National struct {
	// Extra adds local holidays (for example the payer's municipality,
	// which counts for business-day adjustment).
	Extra map[Date]string
}

// Name returns the holiday name for d, or "".
func (n National) Name(d Date) string {
	if s, ok := n.Extra[d]; ok {
		return s
	}
	fixed := map[[2]int]string{
		{1, 1}: "Confraternização Universal", {4, 21}: "Tiradentes", {5, 1}: "Dia do Trabalho",
		{9, 7}: "Independência", {10, 12}: "Nossa Senhora Aparecida", {11, 2}: "Finados",
		{11, 15}: "Proclamação da República", {12, 25}: "Natal",
	}
	if s, ok := fixed[[2]int{int(d.Month), d.Day}]; ok {
		return s
	}
	if d.Month == 11 && d.Day == 20 && d.Year >= 2024 {
		return "Dia Nacional de Zumbi e da Consciência Negra"
	}
	e := Easter(d.Year)
	switch d {
	case e.AddDays(-48):
		return "Carnaval (segunda-feira)"
	case e.AddDays(-47):
		return "Carnaval (terça-feira)"
	case e.AddDays(-2):
		return "Sexta-feira Santa"
	case e.AddDays(60):
		return "Corpus Christi"
	}
	return ""
}

// IsHoliday implements Holidays.
func (n National) IsHoliday(d Date) bool { return n.Name(d) != "" }

// IsBusinessDay reports whether d is a weekday and not a holiday.
func IsBusinessDay(h Holidays, d Date) bool {
	wd := d.Weekday()
	return wd != time.Saturday && wd != time.Sunday && (h == nil || !h.IsHoliday(d))
}

// NextBusinessDay returns d if it is a business day, else the next one.
func NextBusinessDay(h Holidays, d Date) Date {
	for !IsBusinessDay(h, d) {
		d = d.AddDays(1)
	}
	return d
}

// PreviousBusinessDay returns d if it is a business day, else the previous one.
func PreviousBusinessDay(h Holidays, d Date) Date {
	for !IsBusinessDay(h, d) {
		d = d.AddDays(-1)
	}
	return d
}

// Periodicity is the cycle length of a recurrence, with the API Pix names.
type Periodicity string

// Periodicities.
const (
	Weekly     Periodicity = "SEMANAL"
	Monthly    Periodicity = "MENSAL"
	Quarterly  Periodicity = "TRIMESTRAL"
	Semiannual Periodicity = "SEMESTRAL"
	Annual     Periodicity = "ANUAL"
)

// Valid reports whether p is a known periodicity.
func (p Periodicity) Valid() bool {
	switch p {
	case Weekly, Monthly, Quarterly, Semiannual, Annual:
		return true
	}
	return false
}

// CycleStart returns the nominal date of cycle k (0-based) of a recurrence
// starting on start: same weekday for weekly, same day of the month
// (clamped) otherwise.
func CycleStart(start Date, p Periodicity, k int) Date {
	switch p {
	case Weekly:
		return start.AddDays(7 * k)
	case Monthly:
		return addMonths(start, k)
	case Quarterly:
		return addMonths(start, 3*k)
	case Semiannual:
		return addMonths(start, 6*k)
	case Annual:
		return addMonths(start, 12*k)
	}
	panic("calendar: unknown periodicity " + string(p))
}

// DueDate returns the settlement date of cycle k. With adjust, a date that
// is not a business day moves to the next business day, unless that would
// reach the next cycle, in which case it moves back to the previous one:
// the settlement date must always fall inside its own cycle.
func DueDate(h Holidays, start Date, p Periodicity, k int, adjust bool) Date {
	d := CycleStart(start, p, k)
	if !adjust || IsBusinessDay(h, d) {
		return d
	}
	next := NextBusinessDay(h, d)
	if next.Before(CycleStart(start, p, k+1)) {
		return next
	}
	return PreviousBusinessDay(h, d)
}

// CycleOf returns the index of the cycle containing d (-1 before start).
func CycleOf(start Date, p Periodicity, d Date) int {
	if d.Before(start) {
		return -1
	}
	k := 0
	if p == Weekly {
		k = start.DaysUntil(d) / 7
	} else {
		months := map[Periodicity]int{Monthly: 1, Quarterly: 3, Semiannual: 6, Annual: 12}[p]
		k = ((d.Year-start.Year)*12 + int(d.Month) - int(start.Month)) / months
		if k > 0 {
			k--
		}
	}
	for !CycleStart(start, p, k+1).After(d) {
		k++
	}
	return k
}
