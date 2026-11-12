package orchestration

// The words for a cron schedule are adapted from OpenMausBot (Apache License 2.0,
// https://github.com/milind-soni/OpenMausBot): shared/cron-label.ts (cronPreset,
// englishPreset, cronScheduleLabel) and shared/plain-time-zone.ts
// (plainTimeZone). The presets that need the last day of a month ("L") or the
// nth weekday ("#") are left out: the cron parser here takes neither.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	monthNames   = [...]string{
		"January", "February", "March", "April", "May", "June", "July", "August", "September", "October",
		"November", "December",
	}
	digits       = regexp.MustCompile(`^\d+$`)
	universal    = regexp.MustCompile(`(?i)^(?:Etc/)?(?:UTC|UCT|GMT|Universal|Zulu|Greenwich)(?:[+-]0)?$`)
	saidZoneName = map[string]string{
		"Europe/London": "UK time", "Europe/Belfast": "UK time", "America/Phoenix": "Arizona time",
		"Asia/Dubai": "UAE time",
	}
)

// cronPresetWords says a familiar cron expression in words, "every weekday
// at 7:00", or "" when it is not one.
func cronPresetWords(expr string) string {
	f := strings.Fields(expr)
	if len(f) != 5 || !digits.MatchString(f[0]) || !digits.MatchString(f[1]) {
		return ""
	}

	minute, hour, day, month, weekday := f[0], f[1], f[2], f[3], f[4]
	m, _ := strconv.Atoi(minute)
	h, _ := strconv.Atoi(hour)
	at := fmt.Sprintf("%d:%02d", h, m)

	switch {
	case weekday == "*" && month == "*" && day == "*":
		return "every day at " + at
	case weekday == "*" && month == "*" && digits.MatchString(day):
		d, _ := strconv.Atoi(day)
		return fmt.Sprintf("monthly on day %d at %s", d, at)
	case weekday == "*" && digits.MatchString(month) && digits.MatchString(day):
		mo, _ := strconv.Atoi(month)
		if mo < 1 || mo > 12 {
			return ""
		}

		d, _ := strconv.Atoi(day)

		return fmt.Sprintf("yearly on %s %d at %s", monthNames[mo-1], d, at)
	case day != "*" || month != "*":
		return ""
	case weekday == "1-5" || strings.EqualFold(weekday, "MON-FRI"):
		return "every weekday at " + at
	case len(weekday) == 1 && weekday[0] >= '0' && weekday[0] <= '7':
		return fmt.Sprintf("weekly on %s at %s", weekdayNames[int(weekday[0]-'0')%7], at)
	}

	return ""
}

// plainZone says a time zone as people say it: "UTC", "UK time", "Ho Chi
// Minh time". Without the browser's names of zones, the city is
// OpenMausBot's own fallback.
func plainZone(zone string) string {
	zone = strings.TrimSpace(zone)
	if universal.MatchString(zone) {
		return "UTC"
	}

	if said, ok := saidZoneName[zone]; ok {
		return said
	}

	city := zone[strings.LastIndex(zone, "/")+1:]
	if city = strings.TrimSpace(strings.ReplaceAll(city, "_", " ")); city == "" {
		return zone
	}

	return city + " time"
}

// cronWords says a cron schedule: a familiar one in words, any other as its
// expression, and its zone.
func cronWords(expr, zone string) string {
	words := cronPresetWords(expr)
	if words == "" {
		words = "cron " + expr
	}

	return words + " " + plainZone(zone)
}

// SchedulePreview is a schedule in words and its next times.
type SchedulePreview struct {
	Words string
	Next  []time.Time
}

// RoutineSchedulePreviewRuns is how many next times a preview gives (OpenMausBot,
// src/components/routines/cron-editor.ts, nextCronRuns(..., 3)).
const RoutineSchedulePreviewRuns = 3

// PreviewSchedule checks a schedule as PutRoutine would and says it in words
// with its next times, counted from now as for a new Routine.
func (s *Service) PreviewSchedule(everyMinutes int, expr, zone string) (SchedulePreview, error) {
	sched, err := scheduleOf(everyMinutes, strings.TrimSpace(expr), strings.TrimSpace(zone))
	if err != nil {
		return SchedulePreview{}, err
	}

	now := s.clock.Now().UTC()
	if sched.cron == nil {
		sched.anchor = now.Add(sched.every)
	}

	out := SchedulePreview{Words: ScheduleWords(Routine{
		EveryMinutes: everyMinutes, Cron: strings.TrimSpace(expr), Timezone: strings.TrimSpace(zone),
	})}

	at := now
	for range RoutineSchedulePreviewRuns {
		at = sched.next(at)
		out.Next = append(out.Next, at)
	}

	return out, nil
}
