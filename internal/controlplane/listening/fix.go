package listening

import (
	"strings"
	"time"
)

// Fix is the one step that fixes a source's failure, worded once and shared by the Social listening page and
// listen_search's coverage line, so the two never disagree: last30days keeps one registry of (source, failure) fixes
// read by both its doctor and its post-run nudge (lib/prescriptions.py:1-13, lib/quality_nudge.py:6-8). Each code's
// step is what the references prescribe for it: a refusal waits out its rest or is tested again by hand (last30days'
// doctor clears its Reddit skips when asked, doctor.py:2945-2950), a rate limit and a spent quota wait (Agent-Reach
// "cooldown", runtime/manage.py:236-238), a timeout is no sign of a broken source (quality_nudge.py:445-451), a sign-in
// wall needs an account (Agent-Reach "needs_login", manage.py:233-235), and anything else is tested again, then
// reported (Agent-Reach docs/troubleshooting.md:18). until is when its rest ends, zero when it does not rest.
func Fix(source string, code Code, until time.Time) string {
	name := Label(source)
	at := ""

	if !until.IsZero() {
		at = " at " + until.UTC().Format("15:04 UTC, Jan 2")
	}

	switch code {
	case CodeNeedLogin:
		return name + " answers only a signed-in account, and none is set up for it: its other backends serve " +
			"meanwhile."
	case CodeRefused:
		if at == "" {
			at = " after its rest"
		}

		return name + " refused this server's network. It is asked again" + at + "; test it again on the Social " +
			"listening page to try it now."
	case CodeRateLimited:
		if at == "" {
			at = " in an hour"
		}

		return name + " asked to slow down. It is asked again" + at + " by itself."
	case CodeLimitReached:
		if at == "" {
			at = " at 00:00 UTC"
		}

		return name + "'s daily quota is spent. It comes back" + at + " by itself."
	case CodeNotFound:
		return name + " answered that the page asked does not exist: its address may have changed. Test it " +
			"again; if it stays, the backend needs fixing."
	case CodeTimeout:
		return name + " did not answer in time, which is no sign that it is broken. It is asked again in a minute."
	case CodeNoResults:
		return name + " found nothing for its live test, a search that always has results: it may answer empty " +
			"pages to this network. Test it again later."
	case CodeBroken:
		return name + " answered something other than its usual answer: it may have changed. Test it again; if it " +
			"stays broken, the backend needs fixing."
	default:
		return ""
	}
}

// Label is a source's name as a person reads it.
func Label(source string) string {
	switch source {
	case SourceWeb:
		return "The web"
	case SourceReddit:
		return "Reddit"
	}

	if source == "" {
		return source
	}

	return strings.ToUpper(source[:1]) + source[1:]
}

// tells says whether a failure with this code is told to the owner: one that rests and comes back by itself is not,
// as Agent-Reach reports a rate limit as a "cooldown" to wait out, not a broken channel (runtime/manage.py:236-238),
// and last30days says a timeout needs no fix (quality_nudge.py:445-451); postiz tells only the failure a person must
// act on, a refresh that failed (refresh.integration.service.ts:148-166).
func tells(c Code) bool {
	return c != CodeRateLimited && c != CodeLimitReached && c != CodeTimeout
}
