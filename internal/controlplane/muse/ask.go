package muse

// One question to the pool (rows 5, 8, 9, 13, 15-17).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Question is what an agent asks.
type Question struct {
	Prompt string
	// Thread continues a thread of the pool's; empty starts a new one.
	Thread string
	// Deadline bounds the question; zero is 300 s.
	Deadline time.Duration
	// Agent names who asked, as muse.db keeps it.
	Agent string
}

// Result is a question's outcome.
type Result struct {
	Outcome string `json:"outcome"`
	Answer  string `json:"answer,omitempty"`
	Links   []Link `json:"links,omitempty"`
	// Unverified marks an answer that cites no link (row 15).
	Unverified bool   `json:"unverified,omitempty"`
	Files      []File `json:"files,omitempty"`
	Thread     string `json:"thread,omitempty"`
	ThreadURL  string `json:"thread_url,omitempty"`
	Account    string `json:"account"`
	// AccountLabel is the account's name for the owner.
	AccountLabel string `json:"account_label"`
	// Note says what happened when the outcome is not an answer.
	Note string    `json:"note,omitempty"`
	At   time.Time `json:"at"`
}

// Ask asks Muse one question. An error is a question that could not be
// asked at all (no account, a thread that cannot be continued, bad input);
// anything after an account was chosen is the Result's outcome.
func (s *Service) Ask(ctx context.Context, q Question) (Result, error) {
	q.Prompt = strings.TrimSpace(q.Prompt)

	switch {
	case q.Prompt == "":
		return Result{}, fmt.Errorf("%w: the prompt is empty", ErrInvalid)
	case len([]rune(q.Prompt)) > maxPromptChars:
		return Result{}, fmt.Errorf("%w: the prompt is over %d characters", ErrInvalid, maxPromptChars)
	case q.Deadline < 0 || q.Deadline > MaxDeadline:
		return Result{}, fmt.Errorf("%w: the deadline is at most %s", ErrInvalid, MaxDeadline)
	}

	if q.Deadline == 0 {
		q.Deadline = s.t.total
	}

	if q.Thread != "" {
		thread := ThreadID(q.Thread)
		if thread == "" {
			return Result{}, fmt.Errorf("%w: %q is not a Muse thread", ErrInvalid, q.Thread)
		}

		q.Thread = thread
	}

	release, err := s.take(ctx)
	if err != nil {
		return Result{}, err
	}
	defer release()

	// The deadline counts from the question's turn, within the caller's own.
	deadline := time.Now().Add(q.Deadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	candidates, err := s.candidates(ctx, q.Thread)
	if err != nil {
		return Result{}, err
	}

	var refused []string

	for _, a := range candidates {
		res, err := s.askWith(ctx, a, q, deadline)
		if err == nil {
			return res, nil
		}

		// An account that could not be used before anything was sent: a new thread tries the next one.
		refused = append(refused, a.Label+": "+err.Error())

		if q.Thread != "" {
			break
		}
	}

	return Result{}, fmt.Errorf("%w: %s", ErrNoAccount, strings.Join(refused, "; "))
}

// candidates are the accounts that may take the question, in order: the
// thread's own account, or for a new thread every account in the owner's
// order that is on, signed in and not resting. A resting account whose
// Usage was not read for usageEvery is read again first, and taken back when
// it shows room.
func (s *Service) candidates(ctx context.Context, thread string) ([]orchestration.Account, error) {
	accounts, err := s.accounts(ctx)
	if err != nil {
		return nil, err
	}

	if thread != "" {
		return s.threadCandidate(ctx, thread, accounts)
	}

	var (
		out []orchestration.Account
		why []string
	)

	for _, a := range accounts {
		st, err := s.store.state(ctx, a.ID)
		if err != nil {
			return nil, err
		}

		if st.Resting && a.Status == orchestration.AccountActive &&
			s.cfg.Clock.Now().Sub(st.UsageReadAt) >= usageEvery {
			st = s.recheckRest(ctx, a, st)
		}

		switch {
		case a.Status != orchestration.AccountActive:
			why = append(why, a.Label+" is off")
		case st.Resting:
			why = append(why, a.Label+" is resting (out of quota, "+usageText(st)+")")
		case st.State == StateNeedsSignIn:
			why = append(why, a.Label+" needs to be signed in again")
		default:
			// A refused or not ready account is tried again: the question checks it first (row 7).
			out = append(out, a)
		}
	}

	if len(out) == 0 {
		if len(accounts) == 0 {
			return nil, fmt.Errorf("%w: no Muse account was added; add one on Settings → Gateway → Muse", ErrNoAccount)
		}

		return nil, fmt.Errorf("%w: %s", ErrNoAccount, strings.Join(why, "; "))
	}

	return out, nil
}

// threadCandidate is a thread's own account, when it can take the question:
// the thread is never moved to another (row 8).
func (s *Service) threadCandidate(
	ctx context.Context, thread string, accounts []orchestration.Account,
) ([]orchestration.Account, error) {
	owner, err := s.store.threadAccount(ctx, thread)
	if err != nil {
		return nil, err
	}

	if owner == "" {
		return nil, fmt.Errorf("%w: thread %s was not started through Hypomnema; start a new one", ErrInvalid, thread)
	}

	for _, a := range accounts {
		if a.ID != owner {
			continue
		}

		st, err := s.store.state(ctx, a.ID)
		if err != nil {
			return nil, err
		}

		if st.Resting && s.cfg.Clock.Now().Sub(st.UsageReadAt) >= usageEvery {
			st = s.recheckRest(ctx, a, st)
		}

		switch {
		case a.Status != orchestration.AccountActive:
			return nil, fmt.Errorf("%w: the thread's account %s is off; the thread stays with it", ErrNoAccount, a.Label)
		case st.Resting:
			return nil, fmt.Errorf("%w: the thread's account %s is resting (out of quota, %s); the thread stays with "+
				"it: ask again after the reset, or start a new thread", ErrNoAccount, a.Label, usageText(st))
		}

		return []orchestration.Account{a}, nil
	}

	return nil, fmt.Errorf("%w: the thread's account was removed; start a new thread", ErrNoAccount)
}

func usageText(st accountState) string {
	if st.Usage == nil {
		return "resets unknown"
	}

	return st.Usage.Text()
}

// recheckRest reads a resting account's Usage and takes it back when it
// shows room. The caller holds the turn.
func (s *Service) recheckRest(ctx context.Context, a orchestration.Account, st accountState) accountState {
	u, err := s.readUsage(ctx, a.ID)
	if err != nil {
		s.cfg.Logger.InfoContext(ctx, "a resting Muse account's usage could not be read",
			slog.String("event", "muse.usage_unread"), slog.String("account", a.ID), slog.Any("err", err))

		// Read again after usageEvery, not at every question.
		_ = s.store.setUsage(ctx, a.ID, usageOr(st.Usage), s.cfg.Clock.Now())

		return st
	}

	if u.Room() {
		_ = s.store.rest(ctx, a.ID, false, "")
		st.Resting = false
	}

	st.Usage = &u

	return st
}

func usageOr(u *Usage) Usage {
	if u == nil {
		return Usage{}
	}

	return *u
}

// readUsage reads an account's Usage in its tab and keeps it. The caller
// holds the turn.
func (s *Service) readUsage(ctx context.Context, id string) (Usage, error) {
	read, err := s.openJar(ctx, id)
	if err != nil {
		return Usage{}, err
	}

	page, err := s.cfg.Browser.Page(ctx, id, read.cookies)
	if err != nil {
		return Usage{}, err
	}

	d := s.driver(page)

	u, err := d.usage(ctx)
	if errors.Is(err, ErrInterrupted) {
		return Usage{}, err
	}

	s.saveJar(ctx, id, read, page)

	if err != nil {
		return Usage{}, err
	}

	return u, s.store.setUsage(ctx, id, u, s.cfg.Clock.Now())
}

func (s *Service) driver(page Page) *driver {
	// The driver's waits are on the wall clock; the Clock dates what is kept.
	return &driver{page: page, t: s.t, now: time.Now, logger: s.cfg.Logger}
}

// askWith asks with one account. An error is an account that could not be
// used before anything was sent; any later end is the Result's outcome.
func (s *Service) askWith(
	ctx context.Context, a orchestration.Account, q Question, deadline time.Time,
) (Result, error) {
	now := s.cfg.Clock.Now()
	res := Result{Account: a.ID, AccountLabel: a.Label, Thread: q.Thread, At: now}

	read, err := s.openJar(ctx, a.ID)
	if err != nil {
		return Result{}, err
	}

	page, err := s.cfg.Browser.Page(ctx, a.ID, read.cookies)
	if errors.Is(err, browser.ErrHeld) {
		return s.interrupted(ctx, res, q), nil
	}

	if err != nil {
		return Result{}, err
	}

	d := s.driver(page)

	if _, err := d.checkAccount(ctx); err != nil {
		if errors.Is(err, ErrInterrupted) {
			return s.interrupted(ctx, res, q), nil
		}

		s.saveJar(ctx, a.ID, read, page)
		s.noteState(ctx, a.ID, err)

		return Result{}, err
	}

	_ = s.store.setState(ctx, a.ID, StateSignedIn, "", "", s.cfg.Clock.Now())

	got, sent, err := d.ask(ctx, q.Prompt, q.Thread, deadline)
	if got.Thread != "" {
		res.Thread = got.Thread
	}

	switch {
	case errors.Is(err, ErrInterrupted):
		// The owner took the browser: what was read may be what he typed, so nothing of it is kept (row 5).
		return s.interrupted(ctx, res, q), nil
	case err != nil && !sent:
		s.saveJar(ctx, a.ID, read, page)

		if _, isState := stateOf(err); isState {
			s.noteState(ctx, a.ID, err)

			return Result{}, err
		}

		res.Outcome, res.Note = OutcomeError, "Muse was not asked: "+err.Error()
	case err != nil && errors.Is(err, browser.ErrPageGone):
		// Chrome or the tab went away after the send (row 13): the next question makes a new context.
		s.cfg.Browser.DisposeContext(ctx, a.ID)

		res.Outcome, res.Note = OutcomeUnknown, "Sent, result unknown: the browser's tab went away."
	default:
		s.saveJar(ctx, a.ID, read, page)
		s.outcome(ctx, a, &res, got, err)
	}

	if res.Thread != "" {
		res.ThreadURL = ThreadURL(res.Thread)
		_ = s.store.keepThread(ctx, res.Thread, a.ID, now)
	}

	if res.Outcome == OutcomeAnswered && len(got.Media) > 0 {
		res.Files = s.media.keepAll(ctx, a.ID, res.Thread, got.Media, read.cookies, d)
	}

	s.keep(ctx, res, q)

	// The Usage shown on the Muse page is read again after an answer once it is old.
	if res.Outcome == OutcomeAnswered {
		if st, err := s.store.state(ctx, a.ID); err == nil && s.cfg.Clock.Now().Sub(st.UsageReadAt) >= usageEvery {
			if _, err := s.readUsage(ctx, a.ID); err != nil {
				_ = s.store.setUsage(ctx, a.ID, usageOr(st.Usage), s.cfg.Clock.Now())
			}
		}
	}

	return res, nil
}

// outcome is what an ended question is, from the driver's answer and error.
func (s *Service) outcome(ctx context.Context, a orchestration.Account, res *Result, got answer, err error) {
	switch {
	case err == nil:
		res.Outcome, res.Answer, res.Links = OutcomeAnswered, got.Text, got.Links
		res.Unverified = len(got.Links) == 0
	case errors.Is(err, ErrQuota):
		// Out of quota: the account rests until a Usage read shows room (row 9).
		res.Outcome, res.Answer = OutcomeError, got.Text
		res.Note = fmt.Sprintf("Muse account %s is out of quota and rests until its Usage shows room.", a.Label)
		_ = s.store.rest(ctx, a.ID, true, err.Error())

		if _, err := s.readUsage(ctx, a.ID); err != nil {
			_ = s.store.setUsage(ctx, a.ID, Usage{}, s.cfg.Clock.Now())
		}
	case errors.Is(err, ErrAsksPermission):
		// Never clicked: the owner answers it in his own Muse (row 16).
		res.Outcome = OutcomeAsks
		res.Note = "Muse asks: " + got.Approval + "; answer it in Muse (the thread is open in the Muse app of " +
			"account " + a.Label + ")."
	case errors.Is(err, ErrAnswerError):
		res.Outcome, res.Note = OutcomeError, "Muse answered with an error: "+got.Error
	default:
		res.Outcome, res.Note = OutcomeUnknown, "Sent, result unknown: "+err.Error()
	}
}

// interrupted is the answer to a question the owner's hold stopped. Only
// that it was asked and stopped is kept; nothing it read, and nothing goes to
// the vault.
func (s *Service) interrupted(ctx context.Context, res Result, q Question) Result {
	res.Outcome, res.Note = OutcomeInterrupted, "Interrupted by the owner, who took the browser; ask again later."
	res.Answer, res.Links, res.Files = "", nil, nil

	s.keep(ctx, res, q)

	return res
}

// noteState records the state an error shows.
func (s *Service) noteState(ctx context.Context, id string, err error) {
	if state, ok := stateOf(err); ok {
		_ = s.store.setState(ctx, id, state, err.Error(), "", s.cfg.Clock.Now())
	}
}

// keep writes the question to muse.db.
func (s *Service) keep(ctx context.Context, res Result, q Question) {
	_, err := s.store.keep(context.WithoutCancel(ctx), Asked{
		Account: res.Account, Thread: res.Thread, Agent: q.Agent, Prompt: q.Prompt, Answer: res.Answer,
		Links: res.Links, Approval: approvalOf(res), Outcome: res.Outcome, Error: errorOf(res), Files: res.Files,
		At: res.At, Finished: s.cfg.Clock.Now(),
	})
	if err != nil {
		s.cfg.Logger.WarnContext(ctx, "a Muse question could not be kept", slog.String("event", "muse.unkept"),
			slog.Any("err", err))
	}
}

func approvalOf(res Result) string {
	if res.Outcome == OutcomeAsks {
		return res.Note
	}

	return ""
}

func errorOf(res Result) string {
	if res.Outcome == OutcomeAnswered || res.Outcome == OutcomeAsks {
		return ""
	}

	return res.Note
}

// Chats lists the pool's threads, the latest first.
func (s *Service) Chats(ctx context.Context, query string, limit int) ([]Chat, error) {
	return s.store.chats(ctx, query, limit)
}

// Messages lists a thread's questions and answers, or the matches of query.
func (s *Service) Messages(ctx context.Context, thread, query string, limit int) ([]Asked, error) {
	if thread != "" {
		if thread = ThreadID(thread); thread == "" {
			return nil, fmt.Errorf("%w: not a Muse thread", ErrInvalid)
		}
	}

	return s.store.messages(ctx, thread, query, limit)
}
