package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Git hosts and registered repositories (issue #44). Every token reads them;
// only the owner declares a host or registers a repository. A host's token is
// not here: a secret's value never passes through this API (issue #66).

func toGitHost(h orchestration.GitHost) openapi.GitHost {
	out := openapi.GitHost{
		Host: h.Host, Kind: openapi.GitHostKind(h.Kind), Known: h.Known, HasToken: h.HasToken(),
		Connections: h.Connections,
	}
	if out.Connections == nil {
		out.Connections = []string{}
	}

	if !h.Known {
		out.CreatedAt = &h.CreatedAt
	}

	return out
}

func toRepo(r orchestration.Repo) openapi.Repo {
	return openapi.Repo{
		Name: r.Name, Host: r.Host, Path: r.Path, Base: ptr(r.Base), Connection: ptr(r.Connection),
		CreatedAt: r.CreatedAt,
	}
}

// ListGitHosts returns the hosts a Run can be on.
func (s *Server) ListGitHosts(ctx context.Context, _ openapi.ListGitHostsRequestObject,
) (openapi.ListGitHostsResponseObject, error) {
	hosts, err := s.svc.ListGitHosts(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListGitHosts200JSONResponse{Data: make([]openapi.GitHost, 0, len(hosts))}
	for _, h := range hosts {
		out.Data = append(out.Data, toGitHost(h))
	}

	return out, nil
}

// PutGitHost declares a host and its kind.
func (s *Server) PutGitHost(ctx context.Context, req openapi.PutGitHostRequestObject,
) (openapi.PutGitHostResponseObject, error) {
	h, err := s.svc.AddGitHost(ctx, req.Host, githost.Kind(req.Body.Kind))
	if err != nil {
		return nil, err
	}

	return openapi.PutGitHost200JSONResponse(toGitHost(h)), nil
}

// DeleteGitHost forgets a declared host.
func (s *Server) DeleteGitHost(ctx context.Context, req openapi.DeleteGitHostRequestObject,
) (openapi.DeleteGitHostResponseObject, error) {
	// Its token goes with it; without a place for tokens there is none to remove.
	remove := s.svc.RemoveGitHost
	if s.hostTokens != nil {
		remove = s.hostTokens.RemoveHost
	}

	if err := remove(ctx, req.Host); err != nil {
		return nil, err
	}

	return openapi.DeleteGitHost204Response{}, nil
}

// errNoHostTokens answers when the control plane wired no place for tokens.
var errNoHostTokens = fmt.Errorf("%w: this control plane keeps no git host tokens", orchestration.ErrUnavailable)

// connect keeps a token as a Connection of a host and returns the host. The
// token is not logged and is not in the answer.
func (s *Server) connect(ctx context.Context, host, label, token string) (openapi.GitHost, error) {
	if s.hostTokens == nil {
		return openapi.GitHost{}, errNoHostTokens
	}

	if err := s.hostTokens.Connect(ctx, host, label, token); err != nil {
		return openapi.GitHost{}, err
	}

	h, err := s.svc.GitHostOf(ctx, host)
	if err != nil {
		return openapi.GitHost{}, err
	}

	return toGitHost(h), nil
}

func (s *Server) disconnect(ctx context.Context, host, label string) (openapi.GitHost, error) {
	if s.hostTokens == nil {
		return openapi.GitHost{}, errNoHostTokens
	}

	if err := s.hostTokens.Disconnect(ctx, host, label); err != nil {
		return openapi.GitHost{}, err
	}

	h, err := s.svc.GitHostOf(ctx, host)
	if err != nil {
		return openapi.GitHost{}, err
	}

	return toGitHost(h), nil
}

// PutGitHostConnection keeps a token as a Connection of a host, under a label.
func (s *Server) PutGitHostConnection(ctx context.Context, req openapi.PutGitHostConnectionRequestObject,
) (openapi.PutGitHostConnectionResponseObject, error) {
	h, err := s.connect(ctx, req.Host, req.Label, val(req.Body.Token))
	if err != nil {
		return nil, err
	}

	return openapi.PutGitHostConnection200JSONResponse(h), nil
}

// DeleteGitHostConnection forgets a Connection of a host.
func (s *Server) DeleteGitHostConnection(ctx context.Context, req openapi.DeleteGitHostConnectionRequestObject,
) (openapi.DeleteGitHostConnectionResponseObject, error) {
	h, err := s.disconnect(ctx, req.Host, req.Label)
	if err != nil {
		return nil, err
	}

	return openapi.DeleteGitHostConnection200JSONResponse(h), nil
}

// PutGitHostToken keeps a token as the host's Connection "default": the
// form of before a host could have several.
func (s *Server) PutGitHostToken(ctx context.Context, req openapi.PutGitHostTokenRequestObject,
) (openapi.PutGitHostTokenResponseObject, error) {
	h, err := s.connect(ctx, req.Host, orchestration.DefaultConnection, val(req.Body.Token))
	if err != nil {
		return nil, err
	}

	return openapi.PutGitHostToken200JSONResponse(h), nil
}

// DeleteGitHostToken forgets the host's Connection "default".
func (s *Server) DeleteGitHostToken(ctx context.Context, req openapi.DeleteGitHostTokenRequestObject,
) (openapi.DeleteGitHostTokenResponseObject, error) {
	h, err := s.disconnect(ctx, req.Host, orchestration.DefaultConnection)
	if err != nil {
		return nil, err
	}

	return openapi.DeleteGitHostToken200JSONResponse(h), nil
}

// ListRepos returns the registered repositories.
func (s *Server) ListRepos(ctx context.Context, _ openapi.ListReposRequestObject,
) (openapi.ListReposResponseObject, error) {
	repos, err := s.svc.ListRepos(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListRepos200JSONResponse{Data: make([]openapi.Repo, 0, len(repos))}
	for _, r := range repos {
		out.Data = append(out.Data, toRepo(r))
	}

	return out, nil
}

// PutRepo registers a repository under a name.
func (s *Server) PutRepo(ctx context.Context, req openapi.PutRepoRequestObject,
) (openapi.PutRepoResponseObject, error) {
	r, err := s.svc.AddRepo(ctx, req.Name, req.Body.Repo, val(req.Body.Base), val(req.Body.Connection))
	if err != nil {
		return nil, err
	}

	return openapi.PutRepo200JSONResponse(toRepo(r)), nil
}

// DeleteRepo forgets a registered repository.
func (s *Server) DeleteRepo(ctx context.Context, req openapi.DeleteRepoRequestObject,
) (openapi.DeleteRepoResponseObject, error) {
	if err := s.svc.RemoveRepo(ctx, req.Name); err != nil {
		return nil, err
	}

	return openapi.DeleteRepo204Response{}, nil
}
