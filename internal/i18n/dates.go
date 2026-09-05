package i18n

import (
	"fmt"
	"strings"
	"time"
)

// Swedish writes months in lower case and dates as "18 augusti"; English
// capitalises them and says "18 August". Keeping both here means a page never
// mixes the two conventions.
var months = map[Lang][12]string{
	SV: {"januari", "februari", "mars", "april", "maj", "juni",
		"juli", "augusti", "september", "oktober", "november", "december"},
	EN: {"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December"},
}

var monthsShort = map[Lang][12]string{
	SV: {"jan", "feb", "mar", "apr", "maj", "jun", "jul", "aug", "sep", "okt", "nov", "dec"},
	EN: {"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
}

// lang narrows anything unexpected to a language we have tables for.
func lang(l Lang) Lang {
	if l == EN {
		return EN
	}
	return SV
}

// Month returns "augusti" or "August".
func Month(l Lang, t time.Time) string { return months[lang(l)][int(t.Month())-1] }

// MonthShort returns "aug" or "Aug".
func MonthShort(l Lang, t time.Time) string { return monthsShort[lang(l)][int(t.Month())-1] }

// MonthYear renders "augusti 2026" or "August 2026".
func MonthYear(l Lang, t time.Time) string {
	return fmt.Sprintf("%s %d", Month(l, t), t.Year())
}

// DateLong renders "18 augusti 2026" or "18 August 2026" — how a date is
// written in a sentence, when the year matters as much as the day.
func DateLong(l Lang, t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), Month(l, t), t.Year())
}

// DateShort renders "18 aug 2026" or "18 Aug 2026", for a table cell.
func DateShort(l Lang, t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), MonthShort(l, t), t.Year())
}

// DayMonth renders "18 augusti", for a date inside the current year.
func DayMonth(l Lang, t time.Time) string {
	return fmt.Sprintf("%d %s", t.Day(), Month(l, t))
}

// Clock renders "13:00". Both languages use the 24-hour clock: the house is
// in Sweden, and nobody here writes half past one any other way.
func Clock(t time.Time) string { return t.Format("15:04") }

// Stamp renders "18 aug 2026 13:00", for a log line.
func Stamp(l Lang, t time.Time) string {
	return fmt.Sprintf("%s %s", DateShort(l, t), Clock(t))
}

// ISODate renders "2026-08-18", for URLs, date inputs and the spreadsheet
// rather than for reading. It is the same in every language.
func ISODate(t time.Time) string { return t.Format("2006-01-02") }

// TitleCase upper-cases the first rune, for a Swedish month or weekday that
// starts a sentence. English words are already capitalised, so this is a
// no-op there.
func TitleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// Count renders "3 år" or "3 years". The unit names a pair of rows in the
// catalogue, which holds the singular and the plural of each language.
func Count(l Lang, unit string, n int) string {
	return fmt.Sprintf("%d %s", n, Plural(l, unit, n))
}

// Plural returns just the noun, for a sentence that already has the number.
func Plural(l Lang, unit string, n int) string {
	key := "unit." + unit + ".many"
	if n == 1 {
		key = "unit." + unit + ".one"
	}
	return T(l, key)
}

// Tenure renders how long somebody has been a member the way a neighbour
// would say it: "3 år", "1 år 4 månader", "5 månader", "ny sedan i veckan".
// Beyond a year the months are dropped — nobody says "seven years and two
// months a member" — and below a month it becomes a phrase rather than a
// number, because "0 months" reads like a bug.
func Tenure(l Lang, years, months int) string {
	switch {
	case years == 0 && months == 0:
		return T(l, "tenure.new")
	case years == 0:
		return Count(l, "month", months)
	case years < 2 && months > 0:
		return Count(l, "year", years) + " " + Count(l, "month", months)
	default:
		return Count(l, "year", years)
	}
}

// Since renders "sedan i förrgår", "sedan 3 dagar" — how long a
// synchronisation has been failing. A number of days is what the board needs
// to judge whether this is new or has been rotting for a fortnight.
func Since(l Lang, from, now time.Time) string {
	d := now.Sub(from)
	switch {
	case d < time.Minute:
		return T(l, "since.now")
	case d < time.Hour:
		return Count(l, "minute", int(d.Minutes()))
	case d < 48*time.Hour:
		return Count(l, "hour", int(d.Hours()))
	default:
		return Count(l, "day", int(d.Hours()/24))
	}
}

// Money renders "200 kr". The association counts in whole kronor; the ören a
// bank statement carries are stored but never shown, because a fee has never
// once been 199,50.
func Money(kr int) string { return fmt.Sprintf("%d kr", kr) }

// JoinAnd renders a list as "a, b och c" / "a, b and c".
func JoinAnd(l Lang, parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " " + T(l, "and") + " " + parts[len(parts)-1]
}
