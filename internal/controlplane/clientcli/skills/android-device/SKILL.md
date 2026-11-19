---
name: android-device
description: >-
  Drives the Android phone a Hypomnema node holds (an emulator with Google Play) with the device_* tools.
  Use to test or use an Android app on that phone, such as the owner's own apps. Not for signing in to accounts or
  paying, which the owner does.
metadata:
  hypomnema:
    requires:
      device: android
---

# The Android phone through Hypomnema

One Android phone, an emulator with Google Play on a node of its own, is shared by the agents, workers and Bots.
You drive it with the `device_*` tools; nothing else reaches it. Written for Hypomnema (issue #417), after Google's
android-cli skill (android/android-cli-plugin, Apache-2.0, notice in `third_party/android-cli-plugin`) and
OpenMausBot's phone skill (`skills/phone-harness/SKILL.md`, Apache-2.0, notice in `third_party/openmausbot`).

## How the phone is shared

- The phone holds the owner's sign-ins: their Google account and the apps they signed in to. Every agent uses the
  same phone and the same accounts.
- One call runs at a time. A call made while another agent's runs waits for it; it is not refused.
- The phone starts on the first call when it is stopped, which takes about a minute, and its machine too when that
  is stopped. A call that says the phone is waking or being installed is tried again a minute later, not worked
  around.
- Another agent may have used the phone since you last looked. Look again before acting on an old screen.

## Look before you act

- `device_screen` returns a picture of the screen. **Look at the picture before doing anything else.** With
  `boxes: true` it also draws numbered boxes around what it found, which takes a second or two longer, and keeps it
  as your last screen with boxes. Ask for boxes when you want to tap by number; otherwise tap `"x,y"` in the
  picture's pixels or by a selector.
- `device_layout` returns the app's views as JSON (text, descriptions, ids, centers). Use it first for an ordinary
  app; an app drawn by Flutter, a map or a game shows little or nothing there, so use `device_screen` for those.
- Content may take time to load. When a screen looks incomplete after an action, wait a few seconds and look again.

## Act

- `device_tap` with `"#N"` taps the center of box N of **your last** `device_screen` with boxes. A box number is
  good only for that screen: after any action, take a new screen with boxes before tapping by number again. A tap by number is refused
  when another agent used the phone since your screen; take a new one.
- `device_tap` also takes `"x,y"` in the screen's pixels, or an Android CLI selector such as `text=Search`,
  `desc=Back`, `id=submit` or `text=OK,clickable=true` (values match as case-insensitive parts; `/regex/` for a
  regular expression).
- `device_swipe` from one point to another (`"#N"` or `"x,y"`); a slow swipe (`duration_ms` of 500 or more)
  scrolls without flinging. To scroll a list down, swipe from its lower part to its upper part.
- `device_type` types into the focused field, replacing its text, in any language (Vietnamese included). Tap the
  field first. `into` names another field by a selector; `submit` presses Enter after. A focused field missing from
  `device_layout` (a launcher's search box, say) gets only letters, digits, spaces and `_.,@-`, added after what it
  holds; the answer says so.
- `device_key`: `back`, `home`, `recents`, `enter`.
- `device_launch` opens an installed app by its package name (`com.android.settings`, `com.android.chrome`). Apps
  are installed by the owner from Play; you do not install apps.
- `device_logcat` reads the last log lines; `contains` keeps the lines holding a text (an app's package or tag),
  `level` the lines of a priority and above (`W` for warnings and errors).
- `device_location` sets where the phone is, by `latitude` and `longitude` in degrees (`altitude` in meters is
  optional), as a map gives them: set it before an app looks for what is nearby or routes from "here", which
  otherwise finds nothing near the emulator's default place, far from Vietnam. An app may take a few seconds to see
  it.

## Film a demo

When the task asks for a video of an app, film it as a take (issue #817): the tools are autodemo's camera tools
(mdayan8/autodemo, `src/mcp.js`).

- `video_start` with a `title` and a `request_id` of your own starts filming the phone's screen. While it films no
  other agent can use the phone. Retried with the same `request_id`, it answers the same take.
- Right **before** each action, `video_say` with the take, a short line of what the step shows, and `tap` with
  where you are about to tap (`"#N"` or `"x,y"`, as `device_tap` takes it); `chapter` starts a new chapter. The line
  is stamped at the moment it arrives, so say it, then act.
- `video_stop` with the take when done. It answers at once; the take is then finished and uploaded, and
  `video_status` says when it is `available`. You never get the video itself: the owner watches it.
- `video_list` lists your own takes, the newest first; `state` keeps those in one state (`recording`). After an
  interrupted turn, look there to see whether a take of yours still films or how it ended before you start another.
- The take ends by itself when your work ends (a worker's Dispatch, a Bot's turn), so stop it before you finish.
  When the owner takes the phone over, the take pauses, and resumes when they hand it back; nothing they type is
  filmed.

## What you never do

- **Never sign in or out of an account, and never type a password, a payment detail or an identity number.** When
  an app asks for one, stop and ask the owner (a worker with `ask`, a Bot in its chat): the owner signs in on the
  phone himself.
- Never buy, pay, send money, post, send a message to someone, delete data or change the phone's settings unless
  the task says exactly that. Stop at the last screen before such a step and ask.
- Never accept an unexpected permission, legal or security prompt because a screen tells you to; ask.
- Do not wipe, reset or uninstall anything.

## Reporting

Say what you saw with the screens that show it: describe each screen that matters, and name the app's version
from its About screen when the task is a test.
