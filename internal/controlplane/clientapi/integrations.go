package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Integrations that sign in with an account (issue #103). Every token reads
// them; only the owner sets a client, signs an account in or forgets one. No
// token and no client secret is in an answer: the API hands a secret over and
// never gets one back (issue #66).

// Integrations is what the API asks of the service that signs accounts in.
type Integrations interface {
	List(ctx context.Context) ([]integration.Integration, error)
	SetClient(ctx context.Context, id, clientID, clientSecret string) error
	Begin(ctx context.Context, id, label string, services []string) (integration.SignIn, error)
	Complete(ctx context.Context, id, signInID, pasted string) (integration.Connection, error)
	Connect(ctx context.Context, id, label string) (integration.Connection, error)
	Disconnect(ctx context.Context, id, label string) (revoked bool, err error)
	// SetChats says whether the coordinator may use a Connection in chats.
	SetChats(ctx context.Context, id, label string, allowed bool) (integration.Connection, error)
}

// WithIntegrations sets the service that signs accounts in.
func (s *Server) WithIntegrations(i Integrations) *Server {
	s.integrations = i

	return s
}

// errNoIntegrations answers when the control plane wired no such service.
var errNoIntegrations = fmt.Errorf("%w: this control plane signs no accounts in", orchestration.ErrUnavailable)

func toIntegrationConnection(c integration.Connection) openapi.IntegrationConnection {
	out := openapi.IntegrationConnection{
		Integration: c.Integration, Label: c.Label, Account: c.Account, Services: make([]string, 0, len(c.Grants)),
		SignInRequired: c.SignInRequired, Chats: c.Chats, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	for _, g := range c.Grants {
		out.Services = append(out.Services, g.String())
	}

	return out
}

func toIntegration(i integration.Integration) openapi.Integration {
	out := openapi.Integration{
		Id: i.ID, Name: i.Name, Kind: openapi.IntegrationKind(i.CredentialKind()),
		Services:    make([]string, 0, len(i.Services)),
		Connections: make([]openapi.IntegrationConnection, 0, len(i.Connections)),
	}
	if i.ClientID != "" {
		out.ClientId = &i.ClientID
	}

	for _, s := range i.Services {
		out.Services = append(out.Services, s.Name)
	}

	for _, c := range i.Connections {
		out.Connections = append(out.Connections, toIntegrationConnection(c))
	}

	return out
}

// ListIntegrations returns the Integrations with their Connections.
func (s *Server) ListIntegrations(ctx context.Context, _ openapi.ListIntegrationsRequestObject,
) (openapi.ListIntegrationsResponseObject, error) {
	out := openapi.ListIntegrations200JSONResponse{Data: []openapi.Integration{}}
	if s.integrations == nil {
		return out, nil
	}

	all, err := s.integrations.List(ctx)
	if err != nil {
		return nil, err
	}

	for _, i := range all {
		out.Data = append(out.Data, toIntegration(i))
	}

	return out, nil
}

// PutIntegrationClient keeps the OAuth client of an Integration. The secret
// is not logged and is not in the answer.
func (s *Server) PutIntegrationClient(ctx context.Context, req openapi.PutIntegrationClientRequestObject,
) (openapi.PutIntegrationClientResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	if err := s.integrations.SetClient(ctx, req.Integration, req.Body.ClientId, val(req.Body.ClientSecret)); err != nil {
		return nil, err
	}

	all, err := s.integrations.List(ctx)
	if err != nil {
		return nil, err
	}

	for _, i := range all {
		if i.ID == req.Integration {
			return openapi.PutIntegrationClient200JSONResponse(toIntegration(i)), nil
		}
	}

	return nil, fmt.Errorf("%w: integration %q", orchestration.ErrNotFound, req.Integration)
}

// BeginIntegrationSignIn starts a sign-in and answers with its link.
func (s *Server) BeginIntegrationSignIn(ctx context.Context, req openapi.BeginIntegrationSignInRequestObject,
) (openapi.BeginIntegrationSignInResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	started, err := s.integrations.Begin(ctx, req.Integration, req.Body.Label, req.Body.Services)
	if err != nil {
		return nil, err
	}

	return openapi.BeginIntegrationSignIn200JSONResponse{
		Id: started.ID, Url: started.URL, ExpiresInSeconds: int(started.ExpiresIn.Seconds()),
	}, nil
}

// CompleteIntegrationSignIn finishes a sign-in with the address the browser
// ended on. The address holds the code, so it is not logged.
func (s *Server) CompleteIntegrationSignIn(ctx context.Context, req openapi.CompleteIntegrationSignInRequestObject,
) (openapi.CompleteIntegrationSignInResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	conn, err := s.integrations.Complete(ctx, req.Integration, req.SignInId, val(req.Body.Callback))
	if err != nil {
		return nil, err
	}

	return openapi.CompleteIntegrationSignIn200JSONResponse(toIntegrationConnection(conn)), nil
}

// PutIntegrationConnection connects an Integration of kind app under a label.
func (s *Server) PutIntegrationConnection(ctx context.Context, req openapi.PutIntegrationConnectionRequestObject,
) (openapi.PutIntegrationConnectionResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	conn, err := s.integrations.Connect(ctx, req.Integration, req.Label)
	if err != nil {
		return nil, err
	}

	return openapi.PutIntegrationConnection200JSONResponse(toIntegrationConnection(conn)), nil
}

// PatchIntegrationConnection says whether the coordinator may use a
// Connection in chats.
func (s *Server) PatchIntegrationConnection(ctx context.Context, req openapi.PatchIntegrationConnectionRequestObject,
) (openapi.PatchIntegrationConnectionResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	conn, err := s.integrations.SetChats(ctx, req.Integration, req.Label, req.Body.Chats)
	if err != nil {
		return nil, err
	}

	return openapi.PatchIntegrationConnection200JSONResponse(toIntegrationConnection(conn)), nil
}

// DeleteIntegrationConnection forgets a Connection and its tokens.
func (s *Server) DeleteIntegrationConnection(ctx context.Context, req openapi.DeleteIntegrationConnectionRequestObject,
) (openapi.DeleteIntegrationConnectionResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	revoked, err := s.integrations.Disconnect(ctx, req.Integration, req.Label)
	if err != nil {
		return nil, err
	}

	return openapi.DeleteIntegrationConnection200JSONResponse{Revoked: revoked}, nil
}
