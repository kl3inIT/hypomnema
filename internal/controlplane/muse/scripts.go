package muse

// The scripts the driver runs in a muse.ai page. Every one returns a
// JSON-serialisable value and never throws. All knowledge of the page's
// shape is here, as www222fff/muse2api keeps it in one file
// (src/muse2api/drivers/browser/dom.py), so that a change of muse.ai's
// client changes this file. The selectors are those of the three references
// (third_party/muse-chat-mcp, third_party/muse2api-czg,
// third_party/muse2api-www, MIT), read on 2026-11-18; they were not checked
// against the live site in the change that wrote them (issue #782).

import (
	"encoding/json"
	"fmt"
)

// Selectors, Muse-Chat-MCP's (muse-driver.mjs:32-45) and www's (dom.py:16-22).
const (
	selComposer   = `[data-hatch-composer-root] textarea, textarea`
	selStop       = `[data-testid="hatch-composer-stop-button"], button[aria-label*="Stop" i]`
	selMessage    = `[data-message-item][data-message-role="assistant"]`
	selBubble     = `div[class*="hatch-agent-bubble-bg"]`
	selError      = `[data-testid="assistant-response-error-notice"]`
	selApproval   = `[data-hatch-composer-approval-stack], [data-testid="hatch-inline-approval-card"]`
	selHydration  = `[data-hatch-shell-hydration-state]`
	selSettings   = `[data-testid="hatch-dock-more"]`
	selDialog     = `[role=dialog],[aria-modal=true]`
	connectingHit = "Connecting..."
)

// Hints, www's (dom.py:22-24), with what czg and the issue add.
var (
	loginHints = []string{"log in", "sign in", "create an account", "use another account"}
	quotaHints = []string{"out of credits", "usage limit", "limit reached", "token limit", "weekly limit"}
	stallHints = []string{"Still sending", connectingHit}
)

// q quotes a string for a script.
func q(s string) string {
	raw, _ := json.Marshal(s) //nolint:errchkjson // A string.

	return string(raw)
}

// pageState is what the page shows: whether it is ready for a prompt (www's
// PAGE_STATE and FRESH_THREAD, czg's ready check at engine.py:718-739), where
// it is, whether it asks to sign in, and the start of its text.
var pageState = fmt.Sprintf(`(() => {
  const body = document.body ? document.body.innerText : '';
  const ta = document.querySelector(%[1]s);
  const hydration = document.querySelector(%[2]s);
  const hydrated = !hydration || hydration.getAttribute('data-hatch-shell-hydration-state') === 'hydrated';
  const messages = document.querySelectorAll(%[3]s).length + document.querySelectorAll(%[4]s).length;
  return {
    path: location.pathname,
    host: location.hostname,
    ready: document.readyState === 'complete' && !!ta && !ta.disabled && hydrated && !body.includes(%[5]s),
    composer: !!ta,
    empty: messages === 0,
    login: !ta && %[6]s.some(h => body.toLowerCase().includes(h)),
  };
})()`, q(selComposer), q(selHydration), q(selMessage), q(selBubble), q(connectingHit), jsList(loginHints))

// softNewThread opens a blank thread with the page's own router (www's
// SOFT_NEW_THREAD): a full navigation reloads the app and its workspace's
// connection. False when the router is not there.
const softNewThread = `(() => {
  const router = window.next && window.next.router;
  if (!location.hostname.endsWith('muse.ai') || !router || typeof router.push !== 'function') return false;
  router.push('/thread/new');
  return true;
})()`

// composerRect is the center of the composer, or null (czg's engine.py:1131-1141).
var composerRect = fmt.Sprintf(`(() => {
  const t = document.querySelector(%s);
  if (!t) return null;
  t.scrollIntoView({block: 'center', inline: 'nearest'});
  t.focus();
  const r = t.getBoundingClientRect();
  return {x: Math.round(r.left + r.width / 2), y: Math.round(r.top + r.height / 2)};
})()`, q(selComposer))

// setComposer sets the composer's value through the native setter, with input
// and change events, so React takes it (czg's _SETTER_JS); it answers the
// value's length.
func setComposer(text string) string {
	return fmt.Sprintf(`((t) => {
  const ta = document.querySelector(%s);
  if (!ta) return 0;
  ta.focus();
  const s = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
  s.call(ta, t);
  ta.dispatchEvent(new Event('input', {bubbles: true}));
  ta.dispatchEvent(new Event('change', {bubbles: true}));
  return (ta.value || '').length;
})(%s)`, q(selComposer), q(text))
}

// clickSend clicks the Send button found inside the composer, never in the
// whole page, where a history preview may say "Send" (czg's _SEND_BUTTON_JS):
// "clicked", "disabled" or "missing".
var clickSend = fmt.Sprintf(`(() => {
  const visible = e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden';
  let ta = document.activeElement;
  if (!ta || !ta.matches('textarea') || !visible(ta)) ta = [...document.querySelectorAll(%s)].find(visible);
  if (!ta) return 'missing';
  const isSend = b => visible(b) && (/^send( message)?$/i.test((b.getAttribute('aria-label') || '').trim())
    || /^(hatch-composer-send-button|composer-send-button|send-button)$/i.test(b.getAttribute('data-testid') || ''));
  for (let r = ta.parentElement; r && r !== document.body; r = r.parentElement) {
    const b = [...r.querySelectorAll('button,[role="button"]')].find(isSend);
    if (b) {
      if (b.disabled || b.getAttribute('aria-disabled') === 'true') return 'disabled';
      b.click();
      return 'clicked';
    }
    if (r.matches('form,main,[role="main"]')) break;
  }
  return 'missing';
})()`, q(selComposer))

// chatState is the conversation as it is now (www's CHAT_STATE, MCP's wait at
// muse-driver.mjs:403-420): how many answers, the last one's text, whether
// Muse is still answering, a permission card's text, an error notice's text,
// and the stall and delivery notes. It scrolls every inner list to its end,
// so that the last answer is rendered.
var chatState = fmt.Sprintf(`(() => {
  [...document.querySelectorAll('*')].filter(e => {
    const s = getComputedStyle(e);
    return (s.overflowY === 'auto' || s.overflowY === 'scroll') && e.scrollHeight > e.clientHeight + 50;
  }).forEach(e => { e.scrollTop = e.scrollHeight; });
  let answers = [...document.querySelectorAll(%[1]s)];
  if (!answers.length) answers = [...document.querySelectorAll(%[2]s)];
  const last = answers.length ? (answers[answers.length - 1].innerText || '').trim() : '';
  const approval = document.querySelector(%[3]s);
  const notice = document.querySelector(%[4]s);
  const ta = document.querySelector(%[5]s);
  const body = document.body ? document.body.innerText : '';
  return {
    count: answers.length,
    last: last,
    busy: !!document.querySelector(%[6]s),
    approval: approval ? (approval.innerText || '').trim().slice(0, 2000) : '',
    error: notice ? (notice.innerText || '').trim().slice(0, 1000) : '',
    composer: ta ? ta.value : '',
    stalled: %[7]s.some(h => body.includes(h)),
    undelivered: body.includes('Delivery not confirmed'),
  };
})()`, q(selMessage), q(selBubble), q(selApproval), q(selError), q(selComposer), q(selStop), jsList(stallHints))

// readAnswer reads the last answer: its text, and the links and media inside
// it (MCP's readChat at muse-driver.mjs:539-555, www's LAST_BUBBLE_MEDIA),
// saying which selector found it.
var readAnswer = fmt.Sprintf(`(() => {
  let matched = 'message-item';
  let answers = [...document.querySelectorAll(%[1]s)];
  if (!answers.length) { matched = 'bubble'; answers = [...document.querySelectorAll(%[2]s)]; }
  if (!answers.length) return {matched: 'none', text: '', links: [], media: []};
  const b = answers[answers.length - 1];
  const links = [], media = [], seen = new Set();
  b.querySelectorAll('a[href]').forEach(a => {
    if (/^https?:/i.test(a.href) && !seen.has(a.href)) {
      seen.add(a.href);
      links.push({url: a.href, text: (a.innerText || '').trim().slice(0, 300)});
    }
  });
  b.querySelectorAll('img, video, source').forEach(m => {
    const u = m.currentSrc || m.src || '';
    if (/^(https?:|blob:)/i.test(u) && !seen.has(u)) { seen.add(u); media.push(u); }
  });
  return {matched: matched, text: (b.innerText || '').trim(), links: links, media: media};
})()`, q(selMessage), q(selBubble))

// threadPath is the page's path.
const threadPath = `location.pathname`

// authCheck asks muse.ai whether the page's cookies are signed in, from the
// page's own origin (MCP's checkAuth, muse-driver.mjs:162-185).
const authCheck = `(async () => {
  try {
    const res = await fetch('/api/auth/check', {method: 'POST', headers: {'content-type': 'application/json'},
      body: '{}', credentials: 'same-origin'});
    const j = await res.json().catch(() => null);
    return {status: res.status, ok: !!(j && j.ok), viewer: (j && j.viewer_id) ? String(j.viewer_id) : ''};
  } catch (e) { return {status: 0, ok: false, viewer: '', error: String(e)}; }
})()`

// ownerAuthCheck is authCheck in the owner's tab, which may not be on
// muse.ai yet: from elsewhere it answers status -1 without fetching.
const ownerAuthCheck = `((location.hostname === 'muse.ai' || location.hostname.endsWith('.muse.ai')) ? ` +
	authCheck + ` : {status: -1, ok: false, viewer: ''})`

// sessionCheck reads the account's workspace (www's upstream/muse.py:108-119):
// "assigned" is ready; the machine's id and state are for its wake.
const sessionCheck = `(async () => {
  try {
    const res = await fetch('/api/session', {credentials: 'same-origin'});
    const j = await res.json().catch(() => ({}));
    return {status: res.status, session: String(j.status || ''), vmId: String(j.vm_id || ''),
      vm: String(j.vm_state || '')};
  } catch (e) { return {status: 0, session: '', vmId: '', vm: '', error: String(e)}; }
})()`

// vmWake asks muse.ai to wake the account's machine, with the body both
// references send (www's upstream/muse.py:121-128, czg's engine.py:615-619).
func vmWake(vmID string) string {
	return fmt.Sprintf(`(async () => {
  try {
    const res = await fetch('/api/hatch/vm/wake', {method: 'POST', headers: {'content-type': 'application/json'},
      credentials: 'same-origin',
      body: JSON.stringify({vm_id: %s, retry_count: 0, connect_attempt_id: crypto.randomUUID()})});
    return {status: res.status};
  } catch (e) { return {status: 0, error: String(e)}; }
})()`, q(vmID))
}

// Settings → Usage, www's (dom.py:208-244): the dock's Settings button, the
// Settings item of the menu it opens, and the dialog's text.
var (
	settingsEntry = fmt.Sprintf(`(() => {
  const visible = e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden';
  const b = [...document.querySelectorAll(%s)].find(visible)
    || [...document.querySelectorAll('button,[role=button]')]
      .find(e => visible(e) && (e.getAttribute('aria-label') || '').trim() === 'Settings');
  if (!b) return null;
  const r = b.getBoundingClientRect();
  return {x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2)};
})()`, q(selSettings))
	settingsItem = `(() => {
  const visible = e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden';
  const exact = e => (e.textContent || '').trim() === 'Settings';
  const b = [...document.querySelectorAll('[role=menuitem]')]
    .find(e => visible(e) && (exact(e) || [...e.querySelectorAll('span,div')].some(exact)));
  if (!b) return null;
  const r = b.getBoundingClientRect();
  return {x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2)};
})()`
	dialogText = fmt.Sprintf(`(() => {
  const d = [...document.querySelectorAll(%s)]
    .find(e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden');
  return d ? (d.innerText || '') : '';
})()`, q(selDialog))
)

// readBlob reads a blob: address of the page as base64, when it is within
// limit bytes (www's fetch_as_base64): one DevTools message is capped, so a
// larger one is not read.
func readBlob(address string, limit int) string {
	return fmt.Sprintf(`(async () => {
  try {
    const r = await fetch(%s);
    const b = await r.blob();
    if (b.size > %d) return {ok: false, size: b.size, error: 'too large'};
    const buf = new Uint8Array(await b.arrayBuffer());
    let s = '';
    for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
    return {ok: true, mime: b.type || '', size: b.size, data: btoa(s)};
  } catch (e) { return {ok: false, error: String(e)}; }
})()`, q(address), limit)
}

func jsList(items []string) string {
	raw, _ := json.Marshal(items) //nolint:errchkjson // Strings.

	return string(raw)
}
