package browser

// Chrome looks like Chrome to the sites it opens (issue #540). Adapted from
// OpenMausBot (server/browser/chrome/identity.ts, calibrate.ts, flags.ts and
// wrapper.ts, Apache License 2.0, Copyright 2026 Milind Soni and OpenMausBot
// contributors; third_party/openmausbot/). Changed: written in Go; the
// identity is read through the browser's gate but for its own page on
// loopback; the flags reach Chrome through agent-browser's AGENT_BROWSER_ARGS
// and the time zone through its environment, where OpenMausBot writes a
// launcher script; and a binary whose identity cannot be read is not launched.
//
// A headless Chrome tells pages it is headless only through "HeadlessChrome/"
// in its user agent; everything else it reports is what the same binary
// reports headed. So the identity is read from the binary itself, once per
// binary, and only that token changes. The user-agent switch alone empties the
// high-entropy client hints, and a DevTools override alone leaves
// HeadlessChrome in service workers, so the browser gets both: the switch at
// launch, and the override from the sidecar (sidecar.go).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// identityRead bounds reading a binary's identity; OpenMausBot's takes about a second.
	identityRead = time.Minute
	// identityDir holds what was read, a file for each binary.
	identityDir = "identity"
)

// The display pages are told, as a server has none (OpenMausBot's
// SERVER_SCREEN, which a headless --screen-info takes in device pixels).
const serverScreen = "{1920x1080}"

// identityProbe is what a page is asked, in OpenMausBot's words
// (server/browser/cdp/identity.ts, IDENTITY_PROBE).
const identityProbe = `(async () => ({
  userAgent: navigator.userAgent,
  high: await navigator.userAgentData.getHighEntropyValues(["brands", "fullVersionList", "platformVersion",
    "architecture", "bitness", "model", "uaFullVersion", "wow64", "formFactors", "mobile", "platform"]),
}))()`

// brand is one of a browser's brands with its version.
type brand struct {
	Brand   string `json:"brand"`
	Version string `json:"version"`
}

// uaMetadata is DevTools' Emulation.UserAgentMetadata.
type uaMetadata struct {
	Brands          []brand  `json:"brands"`
	FullVersionList []brand  `json:"fullVersionList"`
	FullVersion     string   `json:"fullVersion"`
	Platform        string   `json:"platform"`
	PlatformVersion string   `json:"platformVersion"`
	Architecture    string   `json:"architecture"`
	Model           string   `json:"model"`
	Mobile          bool     `json:"mobile"`
	Bitness         string   `json:"bitness"`
	Wow64           bool     `json:"wow64"`
	FormFactors     []string `json:"formFactors"`
}

// chromeIdentity is what a browser tells pages it is.
type chromeIdentity struct {
	UserAgent         string     `json:"userAgent"`
	UserAgentMetadata uaMetadata `json:"userAgentMetadata"`
}

// probed is what a page reported: navigator.userAgent and its high-entropy
// client hints.
type probed struct {
	UserAgent string `json:"userAgent"`
	High      struct {
		Brands          []brand  `json:"brands"`
		FullVersionList []brand  `json:"fullVersionList"`
		UAFullVersion   *string  `json:"uaFullVersion"`
		Platform        *string  `json:"platform"`
		PlatformVersion *string  `json:"platformVersion"`
		Architecture    *string  `json:"architecture"`
		Model           *string  `json:"model"`
		Mobile          bool     `json:"mobile"`
		Bitness         *string  `json:"bitness"`
		Wow64           bool     `json:"wow64"`
		FormFactors     []string `json:"formFactors"`
	} `json:"high"`
}

var (
	chromeUserAgent = regexp.MustCompile(` Chrome/\d+\.0\.0\.0 `)
	headless        = regexp.MustCompile(`(?i)headless`)
)

// headed is what a headless launch reported, as the same binary reports it
// headed. A build that names itself headless anywhere else, such as the
// headless shell, cannot be made to look like Chrome and is refused.
func headed(p probed) (chromeIdentity, error) {
	userAgent := strings.Replace(p.UserAgent, "HeadlessChrome/", "Chrome/", 1)
	if !chromeUserAgent.MatchString(userAgent) || headless.MatchString(userAgent) {
		return chromeIdentity{}, fmt.Errorf("the browser's user agent is not Chrome's: %s", userAgent)
	}

	h := p.High

	var missing []string

	text := func(value *string, what string) string {
		if value == nil {
			missing = append(missing, what)

			return ""
		}

		return *value
	}

	m := uaMetadata{
		Brands: h.Brands, FullVersionList: h.FullVersionList,
		FullVersion: text(h.UAFullVersion, "full version"), Platform: text(h.Platform, "platform"),
		PlatformVersion: text(h.PlatformVersion, "platform version"),
		Architecture:    text(h.Architecture, "architecture"), Model: text(h.Model, "model"),
		Mobile: h.Mobile, Bitness: text(h.Bitness, "bitness"), Wow64: h.Wow64, FormFactors: h.FormFactors,
	}

	if len(m.Brands) == 0 {
		missing = append(missing, "brands")
	}

	if len(m.FullVersionList) == 0 {
		missing = append(missing, "full version list")
	}

	if len(missing) > 0 {
		return chromeIdentity{}, fmt.Errorf("the browser reported no %s", strings.Join(missing, ", "))
	}

	if m.FormFactors == nil {
		m.FormFactors = []string{}
	}

	for _, b := range append(append([]brand(nil), m.Brands...), m.FullVersionList...) {
		if headless.MatchString(b.Brand) {
			return chromeIdentity{}, errors.New("the browser reports a headless brand")
		}
	}

	return chromeIdentity{UserAgent: userAgent, UserAgentMetadata: m}, nil
}

// valid reports an identity that headed would have made.
func (id chromeIdentity) valid() bool {
	var p probed

	p.UserAgent = id.UserAgent
	m := id.UserAgentMetadata
	p.High.Brands, p.High.FullVersionList = m.Brands, m.FullVersionList
	p.High.UAFullVersion, p.High.Platform, p.High.PlatformVersion = &m.FullVersion, &m.Platform, &m.PlatformVersion
	p.High.Architecture, p.High.Model, p.High.Bitness = &m.Architecture, &m.Model, &m.Bitness
	p.High.Mobile, p.High.Wow64, p.High.FormFactors = m.Mobile, m.Wow64, m.FormFactors

	again, err := headed(p)
	if err != nil {
		return false
	}

	a, _ := json.Marshal(again) //nolint:errchkjson // Strings, booleans and lists of them.
	b, _ := json.Marshal(id)    //nolint:errchkjson // The same.

	return bytes.Equal(a, b)
}

// fingerprint changes when the binary is replaced in place
// (OpenMausBot's chromeFingerprint).
func fingerprint(chrome string) (string, error) {
	resolved, err := filepath.EvalSymlinks(chrome)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256([]byte(resolved + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" +
		strconv.FormatInt(info.ModTime().UnixNano(), 10)))

	return hex.EncodeToString(sum[:12]), nil
}

// identityFile is where the identity of a binary is kept.
func identityFile(dir, chrome string) (string, error) {
	sum, err := fingerprint(chrome)
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, identityDir, sum+".json"), nil
}

// identityOf is the identity of the binary: the one kept for it, or read
// from it and kept.
func (e *Engine) identityOf(ctx context.Context, gateArgs []string) (chromeIdentity, error) {
	file, err := identityFile(e.cfg.Dir, e.chrome)
	if err != nil {
		return chromeIdentity{}, err
	}

	var kept chromeIdentity
	//nolint:gosec // The engine's own file.
	if raw, err := os.ReadFile(file); err == nil && json.Unmarshal(raw, &kept) == nil && kept.valid() {
		return kept, nil
	}

	id, err := e.readIdentity(ctx, gateArgs)
	if err != nil {
		return chromeIdentity{}, err
	}

	raw, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return chromeIdentity{}, err
	}

	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return chromeIdentity{}, err
	}

	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return chromeIdentity{}, err
	}

	if err := os.Rename(tmp, file); err != nil {
		return chromeIdentity{}, err
	}

	e.cfg.Logger.InfoContext(ctx, "the browser's identity was read",
		slog.String("event", "browser.identity"), slog.String("user_agent", id.UserAgent))

	return id, nil
}

// readIdentity starts the binary headless on a throwaway profile, with no
// override, and asks a page of its own on loopback what it is: loopback is a
// secure context, where navigator.userAgentData exists. Chrome goes through
// the gate for everything else (gateArgs); loopback, where only its own
// page is opened, it reaches directly, as Chrome does unless told otherwise.
func (e *Engine) readIdentity(ctx context.Context, gateArgs []string) (chromeIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, identityRead)
	defer cancel()

	page, closePage, err := loopbackPage(ctx)
	if err != nil {
		return chromeIdentity{}, err
	}
	defer closePage()

	profile, err := os.MkdirTemp(e.cfg.Dir, "identity-")
	if err != nil {
		return chromeIdentity{}, err
	}
	defer os.RemoveAll(profile) //nolint:errcheck // A throwaway profile.

	args := append([]string{
		"--headless=new", "--remote-debugging-port=0", "--user-data-dir=" + profile,
		"--no-first-run", "--no-default-browser-check",
	}, gateArgs...)
	if e.cfg.NoSandbox {
		args = append(args, "--no-sandbox")
	}

	var stderr capped

	//nolint:gosec // The pinned browser, with arguments the engine wrote.
	cmd := exec.CommandContext(ctx, e.chrome, append(args, "about:blank")...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.home()}
	cmd.Stderr = &stderr
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Start(); err != nil {
		return chromeIdentity{}, fmt.Errorf("starting Chrome to read what it is: %w", err)
	}

	exited := make(chan struct{})

	go func() {
		_ = cmd.Wait()

		close(exited)
	}()

	defer func() {
		_ = cmd.Process.Kill()

		<-exited
	}()

	address, err := devToolsAddress(ctx, profile, exited)
	if err != nil {
		text := stderr.buf.String()
		if hint := sandboxHint(text); hint != "" {
			return chromeIdentity{}, errors.New(hint)
		}

		return chromeIdentity{}, fmt.Errorf("%w: %s", err, lastLine(text))
	}

	conn, err := dialCDP(ctx, address, nil)
	if err != nil {
		return chromeIdentity{}, err
	}
	defer conn.close()

	got, err := probe(ctx, conn, page)
	if err != nil {
		return chromeIdentity{}, err
	}

	return headed(got)
}

// devToolsAddress waits for the file in which Chrome writes its DevTools port
// and path.
func devToolsAddress(ctx context.Context, profile string, exited <-chan struct{}) (string, error) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	for {
		//nolint:gosec // Chrome's own file, in the throwaway profile.
		if raw, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil {
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) == 2 {
				return "ws://127.0.0.1:" + strings.TrimSpace(lines[0]) + strings.TrimSpace(lines[1]), nil
			}
		}

		select {
		case <-exited:
			return "", errors.New("chrome ended before it gave its DevTools address")
		case <-ctx.Done():
			return "", fmt.Errorf("chrome gave no DevTools address: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// loopbackPage serves a blank page on a loopback port of its own.
func loopbackPage(ctx context.Context) (string, func(), error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte("<!doctype html><title>identity</title>"))
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() { _ = srv.Serve(listener) }()

	return "http://" + listener.Addr().String() + "/", func() { _ = srv.Close() }, nil
}

// probe opens the page in a tab of its own and asks it what the browser is,
// once it has loaded.
func probe(ctx context.Context, conn *cdpConn, page string) (probed, error) {
	var created struct {
		TargetID string `json:"targetId"`
	}

	if err := conn.send(ctx, "Target.createTarget", map[string]any{"url": page}, "", &created); err != nil {
		return probed{}, err
	}

	var attached struct {
		SessionID string `json:"sessionId"`
	}

	if err := conn.send(ctx, "Target.attachToTarget",
		map[string]any{"targetId": created.TargetID, "flatten": true}, "", &attached); err != nil {
		return probed{}, err
	}

	pageJSON, _ := json.Marshal(page) //nolint:errchkjson // A string.
	expression := "location.href === " + string(pageJSON) + ` && document.readyState === "complete" ? ` +
		identityProbe + " : null"

	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		var answer struct {
			Result struct {
				Value *probed `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text string `json:"text"`
			} `json:"exceptionDetails"`
		}

		err := conn.send(ctx, "Runtime.evaluate", map[string]any{
			"expression": expression, "awaitPromise": true, "returnByValue": true,
		}, attached.SessionID, &answer)

		switch {
		case err != nil && ctx.Err() != nil:
			return probed{}, fmt.Errorf("reading what the browser is: %w", err)
		case err == nil && answer.ExceptionDetails != nil:
			return probed{}, fmt.Errorf("reading what the browser is: %s", answer.ExceptionDetails.Text)
		case err == nil && answer.Result.Value != nil:
			return *answer.Result.Value, nil
		}

		// The page has not loaded yet, or the navigation replaced the context the question was asked in.
		select {
		case <-ctx.Done():
			return probed{}, fmt.Errorf("the page did not load to read what the browser is: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// identityArgs are Chrome's switches for its identity, put last so that they
// win over any switch of the same name (OpenMausBot's launcherFlags): no
// AutomationControlled, the headed user agent, a display, the language.
func identityArgs(id chromeIdentity, language string) []string {
	args := []string{
		"--disable-blink-features=AutomationControlled",
		"--user-agent=" + id.UserAgent,
		"--screen-info=" + serverScreen,
	}

	if language != "" {
		args = append(args, "--lang="+language)
	}

	return args
}

var (
	languagePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
	posixLocale     = regexp.MustCompile(`^([a-z]{2,3})(?:_([A-Z]{2}))?(?:[.@]|$)`)
	cLocale         = regexp.MustCompile(`^(?:C|POSIX)(?:[.@].*)?$`)
	zonePattern     = regexp.MustCompile(`^:?[A-Za-z0-9_+\-./]{1,64}$`)
)

// Language is the language the browser's pages are told, as OpenMausBot
// chooses it (browserLanguage): the one named, else the system's locale; "C"
// and "POSIX" name none and give en-US. Empty: Chrome's own.
func Language(named string, getenv func(string) string) string {
	if named = strings.TrimSpace(named); languagePattern.MatchString(named) {
		return named
	}

	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if locale = strings.TrimSpace(getenv(name)); locale != "" {
			break
		}
	}

	if cLocale.MatchString(locale) {
		return "en-US"
	}

	match := posixLocale.FindStringSubmatch(locale)
	switch {
	case match == nil:
		return ""
	case match[2] != "":
		return match[1] + "-" + match[2]
	default:
		return match[1]
	}
}

// TimeZone is the zone the browser tells time in, as OpenMausBot chooses it
// (browserTimeZone): the one named, else the system's TZ. Empty: the system's.
func TimeZone(named string, getenv func(string) string) string {
	zone := strings.TrimSpace(named)
	if zone == "" {
		zone = strings.TrimSpace(getenv("TZ"))
	}

	if !zonePattern.MatchString(zone) {
		return ""
	}

	return zone
}

// languageList is the languages pages are told, best first: "pt-BR" gives
// "pt-BR,pt,en-US,en", none "en-US,en" (OpenMausBot's languageList). A
// headless Chrome on Linux tells pages only its first language otherwise.
func languageList(language string) string {
	first := language
	if first == "" {
		first = "en-US"
	}

	base, _, _ := strings.Cut(first, "-")

	list := []string{first}
	if base != first {
		list = append(list, base)
	}

	if base != "en" {
		list = append(list, "en-US", "en")
	}

	return strings.Join(list, ",")
}
