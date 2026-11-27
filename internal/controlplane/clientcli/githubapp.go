package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/ciwait"
)

// The owner's GitHub App (issue #1262) from the terminal: its state, its installations read again, a test delivery,
// and the wait for a pull request's CI on its events.

func printGitHubApp(e *env, fs *flag.FlagSet, app *openapi.GitHubApp) error {
	return e.print(fs, app, func() {
		if !app.Registered {
			e.printf("no GitHub App is registered; open %s in your signed-in browser\n", app.SetupUrl)

			return
		}

		e.printf("app\t%s\t%d\t%s\n", val(app.Slug), val(app.AppId), val(app.HtmlUrl))

		for _, i := range app.Installations {
			e.printf("installed\t%s\t%d\t%s\n", i.Repository, i.InstallationId, i.Account)
		}

		if len(app.Installations) == 0 {
			e.printf("installed on no repository yet: %s\n", val(app.InstallUrl))
		}

		if d := app.LastDelivery; d != nil {
			e.printf("last delivery\t%s\t%s\t%s\n", d.Event, val(d.Action), d.ReceivedAt.Format(time.RFC3339))
		}
	})
}

func hostsApp(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetGitHubAppWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the GitHub App: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the GitHub App", resp.StatusCode(), resp.JSONDefault)
	}

	return printGitHubApp(e, fs, resp.JSON200)
}

func hostsAppSync(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.SyncGitHubAppInstallationsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the GitHub App's installations: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the GitHub App's installations", resp.StatusCode(), resp.JSONDefault)
	}

	return printGitHubApp(e, fs, resp.JSON200)
}

func hostsAppTest(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.TestGitHubAppDeliveryWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("asking for a test delivery: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("asking for a test delivery", resp.StatusCode(), resp.JSONDefault)
	}

	d := resp.JSON200

	return e.print(fs, d, func() {
		e.printf("GitHub sends %s delivery %s again, with its id: it is answered as handled before; the App's "+
			"Recent Deliveries on GitHub shows the answer\n", d.Event, d.Guid)
	})
}

// ciWait waits for a pull request's CI on the App's events (`hypomnema ci wait <pr>`), asking the client API again
// and again, each ask held there a while (ciwait). It calls no GitHub; a repository the App does not cover is waited
// for with gh every 60 s, as before, within the same --timeout. Its exit code is gh pr checks': 0 passed, 1 failed,
// 8 still pending at --timeout.
func ciWait(ctx context.Context, e *env, fs *flag.FlagSet) error {
	pr := fs.Arg(0)

	timeout, err := time.ParseDuration(str(fs, "timeout"))
	if err != nil || timeout < 0 {
		return errors.New("--timeout is a duration such as 90m")
	}

	ask := func(ctx context.Context, hold time.Duration) (ciwait.State, error) {
		wait := int(hold.Seconds())

		resp, err := e.api.WaitPullRequestCIWithResponse(ctx, &openapi.WaitPullRequestCIParams{
			PullRequest: pr, Wait: &wait,
		})
		if err != nil {
			return ciwait.State{}, fmt.Errorf("waiting for the CI of %s: %w", pr, err)
		}

		if resp.JSON200 == nil {
			err := apiError("waiting for the CI of "+pr, resp.StatusCode(), resp.JSONDefault)
			if resp.StatusCode() == http.StatusConflict || resp.StatusCode() == http.StatusNotFound ||
				resp.StatusCode() == http.StatusBadRequest {
				return ciwait.State{}, ciwait.Stop(&notCoveredError{err: err, code: resp.StatusCode()})
			}

			return ciwait.State{}, err
		}

		st := ciwait.State{PullRequest: resp.JSON200.PullRequest, Head: val(resp.JSON200.Head),
			State: string(resp.JSON200.State)}
		if resp.JSON200.Failed != nil {
			st.Failed = *resp.JSON200.Failed
		}

		return st, nil
	}

	return ciwait.Run(ctx, pr, ask, timeout, func(err error) bool {
		var nc *notCoveredError

		return errors.As(err, &nc) && nc.code != http.StatusBadRequest
	}, e.out, e.out)
}

// notCoveredError is the client API's answer that it cannot wait for this pull request: the App does not cover its
// repository (409), the control plane has no App (404), or the pull request is not one on github.com (400).
type notCoveredError struct {
	err  error
	code int
}

func (n *notCoveredError) Error() string { return n.err.Error() }
