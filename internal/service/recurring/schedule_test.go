package recurring

import (
	"testing"
	"time"
)

// TestNextOccurrenceAfter_SameDayCreationSkipsToNextMonth guards the
// deliberate "strictly after, not on or after" rule: setting one up ON its
// own day-of-month must schedule next month's occurrence, never today's —
// otherwise a scheduler tick minutes after creation would immediately
// double-record something the user is just setting up.
func TestNextOccurrenceAfter_SameDayCreationSkipsToNextMonth(t *testing.T) {
	from := time.Date(2026, time.March, 5, 14, 30, 0, 0, time.UTC)
	got := nextOccurrenceAfter(from, 5)
	want := time.Date(2026, time.April, 5, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("nextOccurrenceAfter(%v, 5) = %v, want %v", from, got, want)
	}
}

// TestNextOccurrenceAfter_LaterThisMonthStaysThisMonth covers the ordinary
// case: creating a recurring transaction before its day-of-month has
// happened yet this month schedules THIS month's occurrence, not next
// month's.
func TestNextOccurrenceAfter_LaterThisMonthStaysThisMonth(t *testing.T) {
	from := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	got := nextOccurrenceAfter(from, 20)
	want := time.Date(2026, time.March, 20, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("nextOccurrenceAfter(%v, 20) = %v, want %v", from, got, want)
	}
}

// TestNextOccurrenceAfter_DecemberRollsIntoJanuary covers the year-boundary
// case, relying on time.Date's own month-overflow normalization rather than
// any special-cased branch in occurrenceIn/nextOccurrenceAfter.
func TestNextOccurrenceAfter_DecemberRollsIntoJanuary(t *testing.T) {
	from := time.Date(2026, time.December, 25, 0, 0, 0, 0, time.UTC)
	got := nextOccurrenceAfter(from, 5)
	want := time.Date(2027, time.January, 5, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("nextOccurrenceAfter(%v, 5) = %v, want %v", from, got, want)
	}
}

// TestNextOccurrenceAfter_Day31ClampsToShortMonthsIncludingFebruary covers
// "end of month" semantics: day_of_month 31 must resolve to whatever the
// target month's own last day actually is, non-leap February included.
func TestNextOccurrenceAfter_Day31ClampsToShortMonthsIncludingFebruary(t *testing.T) {
	cases := []struct {
		name string
		from time.Time
		want time.Time
	}{
		{
			"April has 30 days",
			time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, time.April, 30, 0, 0, 0, 0, time.UTC),
		},
		{
			"non-leap February has 28 days",
			time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, time.February, 28, 0, 0, 0, 0, time.UTC),
		},
		{
			"leap February has 29 days",
			time.Date(2028, time.February, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2028, time.February, 29, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextOccurrenceAfter(c.from, 31)
			if !got.Equal(c.want) {
				t.Errorf("nextOccurrenceAfter(%v, 31) = %v, want %v", c.from, got, c.want)
			}
		})
	}
}

// TestResolveBucket covers the recognized/unrecognized split — an empty or
// garbage Bucket value must be nil (no override, falls back to the
// category's own default), never a bogus non-nil value.
func TestResolveBucket(t *testing.T) {
	if got := resolveBucket("needs"); got == nil || *got != "needs" {
		t.Errorf("resolveBucket(needs) = %v, want a pointer to \"needs\"", got)
	}
	if got := resolveBucket("WANTS"); got == nil || *got != "wants" {
		t.Errorf("resolveBucket(WANTS) = %v, want a pointer to \"wants\" (case-insensitive)", got)
	}
	if got := resolveBucket(""); got != nil {
		t.Errorf("resolveBucket(\"\") = %v, want nil", got)
	}
	if got := resolveBucket("not-a-real-bucket"); got != nil {
		t.Errorf("resolveBucket(garbage) = %v, want nil", got)
	}
}
