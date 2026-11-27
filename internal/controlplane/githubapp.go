package controlplane

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp"
)

// clientGitHubApp gives the client API the owner's GitHub App (issue #1262) in its own shapes.
type clientGitHubApp struct{ *githubapp.App }

func (c clientGitHubApp) Status(ctx context.Context) (clientapi.GitHubAppStatus, error) {
	st, err := c.App.Status(ctx)

	return clientapi.GitHubAppStatus(st), err
}
