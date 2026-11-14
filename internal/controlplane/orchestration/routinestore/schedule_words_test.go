package routinestore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// t0 is the root's tests' clock: Saturday 09:00 UTC.
var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// A schedule reads as words: a familiar cron expression as what it means and
// any other as itself, with its zone as people say it (OpenMausBot's
// cron-label.ts and plain-time-zone.ts).
func TestAScheduleInWords(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		every int
		cron  string
		zone  string
		want  string
	}{
		{every: 60, want: "every hour"},
		{every: 15, want: "every 15 minutes"},
		{every: 180, want: "every 3 hours"},
		{every: 1440, want: "every day"},
		{cron: "0 9 * * *", zone: "Asia/Ho_Chi_Minh", want: "every day at 9:00 Ho Chi Minh time"},
		{cron: "30 7 * * 1-5", zone: "Europe/London", want: "every weekday at 7:30 UK time"},
		{cron: "0 8 * * MON-FRI", zone: "UTC", want: "every weekday at 8:00 UTC"},
		{cron: "0 17 * * 5", zone: "America/New_York", want: "weekly on Friday at 17:00 New York time"},
		{cron: "0 6 * * 7", zone: "Etc/UTC", want: "weekly on Sunday at 6:00 UTC"},
		{cron: "5 0 1 * *", zone: "Asia/Tokyo", want: "monthly on day 1 at 0:05 Tokyo time"},
		{cron: "0 12 25 12 *", zone: "Europe/Paris", want: "yearly on December 25 at 12:00 Paris time"},
		{cron: "*/20 9-17 * * 1-5", zone: "Asia/Ho_Chi_Minh", want: "cron */20 9-17 * * 1-5 Ho Chi Minh time"},
		{cron: "0 9 1 * 1", zone: "UTC", want: "cron 0 9 1 * 1 UTC"},
	} {
		got := ScheduleWords(Routine{EveryMinutes: tc.every, Cron: tc.cron, Timezone: tc.zone})
		assert.Equal(t, tc.want, got, "%d %q %q", tc.every, tc.cron, tc.zone)
	}
}

// Scenario: the owner writes a schedule in the form. Before it is kept, the
// control plane says it in words with its next three times, as a Routine
// made now would have them; a schedule a Routine may not have is refused
// with the same reason PutRoutine gives.
func TestAScheduleIsPreviewedBeforeItIsKept(t *testing.T) {
	t.Parallel()

	// A preview reads the clock only.
	s := New(nil, fixedClock{t0}, nil, nil)

	hourly, err := s.PreviewSchedule(60, "", "")
	require.NoError(t, err)
	assert.Equal(t, "every hour", hourly.Words)
	assert.Equal(t, []time.Time{t0.Add(time.Hour), t0.Add(2 * time.Hour), t0.Add(3 * time.Hour)}, hourly.Next)

	// t0 is Saturday 09:00 UTC, 16:00 in Ho Chi Minh City (UTC+7).
	weekdays, err := s.PreviewSchedule(0, " 0 7 * * 1-5 ", "Asia/Ho_Chi_Minh")
	require.NoError(t, err)
	assert.Equal(t, "every weekday at 7:00 Ho Chi Minh time", weekdays.Words)
	assert.Equal(t, []time.Time{
		time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
	}, weekdays.Next)

	for _, bad := range []struct {
		every      int
		cron, zone string
		reason     string
	}{
		{every: 4, reason: "every 5 to 1440 minutes"},
		{cron: "0 7 * * *", reason: "needs its time zone"},
		{cron: "0 7 * *", zone: "UTC", reason: "five fields"},
		{cron: "0 7 * * *", zone: "Mars/Olympus", reason: "not an IANA time zone"},
	} {
		_, err := s.PreviewSchedule(bad.every, bad.cron, bad.zone)
		require.ErrorIs(t, err, storeerr.ErrInvalid)
		assert.ErrorContains(t, err, bad.reason)
	}
}
