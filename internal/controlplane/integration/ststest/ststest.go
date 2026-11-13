// Package ststest is a stand-in for AWS's STS that answers AssumeRole as STS
// does, for the tests of the AWS Integration (issue #331).
package ststest

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// STS assumes a role only with a key it trusts, for the session asked, and
// says when the session ends.
type STS struct {
	// URL is where it answers.
	URL string

	mu     sync.Mutex
	trusts map[string]string
	lasts  time.Duration
	asked  []string
}

// New starts a stand-in whose roles trust the keys named: role ARN to access
// key id. Its sessions last an hour.
func New(t *testing.T, trusts map[string]string) *STS {
	t.Helper()

	f := &STS{trusts: trusts, lasts: time.Hour}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.URL = server.URL

	return f
}

// Trust replaces which key each role trusts.
func (f *STS) Trust(trusts map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.trusts = trusts
}

// Lasts sets how long the sessions it gives from now on last.
func (f *STS) Lasts(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lasts = d
}

// Assumed are the roles assumed, in order, each as "ROLE by KEY for Ns as NAME".
func (f *STS) Assumed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.asked...)
}

func (f *STS) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Form.Get("Action") != "AssumeRole" {
		http.Error(w, "not AssumeRole", http.StatusBadRequest)

		return
	}

	// The access key id is the first part of the signature's credential scope.
	key := ""
	if _, after, ok := strings.Cut(r.Header.Get("Authorization"), "Credential="); ok {
		key, _, _ = strings.Cut(after, "/")
	}

	// What is said back is escaped: it came in the request.
	role := html.EscapeString(r.Form.Get("RoleArn"))
	key = html.EscapeString(key)

	f.mu.Lock()
	f.asked = append(f.asked, fmt.Sprintf("%s by %s for %ss as %s", role, key, r.Form.Get("DurationSeconds"),
		r.Form.Get("RoleSessionName")))
	trusted, lasts, n := f.trusts[role] == key, f.lasts, len(f.asked)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/xml")

	if !trusted {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type>`+
			`<Code>AccessDenied</Code><Message>User %s is not authorized to perform: sts:AssumeRole on %s</Message>`+
			`</Error><RequestId>r1</RequestId></ErrorResponse>`, key, role)

		return
	}

	_, _ = fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>`+
		`<Credentials><AccessKeyId>ASIASESSION%d</AccessKeyId><SecretAccessKey>session-secret</SecretAccessKey>`+
		`<SessionToken>session-token</SessionToken><Expiration>%s</Expiration></Credentials>`+
		`<AssumedRoleUser><Arn>%s/hypomnema</Arn><AssumedRoleId>AROA:hypomnema</AssumedRoleId></AssumedRoleUser>`+
		`</AssumeRoleResult><ResponseMetadata><RequestId>r1</RequestId></ResponseMetadata></AssumeRoleResponse>`,
		n, time.Now().Add(lasts).UTC().Format(time.RFC3339), role)
}
