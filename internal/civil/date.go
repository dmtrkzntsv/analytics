// Package civil provides a timezone-free calendar date. All analytics
// bucketing happens on UTC calendar days (spec §8, §9).
package civil

import (
	"fmt"
	"time"
)

type Date struct {
	Year  int
	Month time.Month
	Day   int
}

func DateOf(t time.Time) Date {
	u := t.UTC()
	return Date{u.Year(), u.Month(), u.Day()}
}

func Today(now time.Time) Date { return DateOf(now) }

func Parse(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return Date{}, fmt.Errorf("civil: parse %q: %w", s, err)
	}
	return DateOf(t), nil
}

func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

func (d Date) Time() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

func (d Date) AddDays(n int) Date { return DateOf(d.Time().AddDate(0, 0, n)) }

func (d Date) Before(o Date) bool { return d.Time().Before(o.Time()) }
