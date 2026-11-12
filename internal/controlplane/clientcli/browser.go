package clientcli

import (
	"context"
	"flag"
	"fmt"
)

// browserStatus prints what the shared browser is doing and its tabs
// (issue #219): one line for the browser, then one for each tab.
func browserStatus(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetBrowserWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("asking for the browser: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("asking for the browser", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		b := resp.JSON200

		state := "not installed yet"

		switch {
		case b.Running:
			state = "running"
		case b.Installed:
			state = "not running"
		}

		driver := "agents drive it"
		if b.Held {
			driver = "the owner holds it"
		}

		e.printf("%s, %s, %d tabs\n", state, driver, len(b.Tabs))

		for _, t := range b.Tabs {
			whose := string(t.Kind)
			if t.TaskId != nil {
				whose += " " + *t.TaskId
			}

			e.printf("%s\t%s\t%s\t%s\n", t.Id, whose, t.Url, t.Title)
		}
	})
}
