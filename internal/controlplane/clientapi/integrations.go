package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
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
	// BeginAsUser starts the owner's own sign-in to an app Integration through one of its app Connections.
	BeginAsUser(ctx context.Context, id, label, app string) (integration.SignIn, error)
	Complete(ctx context.Context, id, signInID, pasted string) (integration.Connection, error)
	// Code answers the current QR code of a linking in progress, for an Integration of kind device.
	Code(ctx context.Context, id, signInID string) (integration.SignIn, error)
	// Connect connects an Integration that is an app: each Connection is an app of its own.
	Connect(ctx context.Context, id, label string, app integration.App) (integration.Connection, error)
	// ConnectAccount connects an Integration of kind account: an address, a user name and a password.
	ConnectAccount(ctx context.Context, id, label string, a integration.Account) (integration.Connection, error)
	// ConnectRole connects an Integration of kind role: a role of an AWS account and the key that assumes it.
	ConnectRole(ctx context.Context, id, label string, r integration.Role) (integration.Connection, error)
	Disconnect(ctx context.Context, id, label string) (revoked bool, err error)
	// SetChats says whether the coordinator may use a Connection in chats.
	SetChats(ctx context.Context, id, label string, allowed bool) (integration.Connection, error)
	// Check reads again what a Connection's account can do at the service (issue #567).
	Check(ctx context.Context, id, label string) (integration.Connection, error)
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
		Address: opt(c.Address), Version: opt(c.Version), AppId: opt(c.AppID), App: opt(c.AppLabel),
		RoleArn: opt(c.RoleARN), Key: opt(c.Key), Cannot: []openapi.ConnectionCannot{},
	}

	for _, no := range serviceproxy.CannotOf(c.Integration, c.Access) {
		out.Cannot = append(out.Cannot, openapi.ConnectionCannot{
			Action: no.Action.ID, Service: no.Action.Service, Name: no.Action.Name, Reason: no.Reason,
		})
	}

	if st := c.Session; st != nil {
		out.Session = &openapi.DeviceSession{State: openapi.DeviceSessionState(st.Status), Reason: opt(st.Reason)}
		if !st.Until.IsZero() {
			out.Session.Until = &st.Until
		}
	}

	if !c.Access.CheckedAt.IsZero() {
		out.AccessCheckedAt = &c.Access.CheckedAt
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

// BeginIntegrationSignIn starts a sign-in and answers with its link: an
// account's, or with app the owner's own through that app Connection, which
// has a code to enter (issue #468).
func (s *Server) BeginIntegrationSignIn(ctx context.Context, req openapi.BeginIntegrationSignInRequestObject,
) (openapi.BeginIntegrationSignInResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	var (
		started integration.SignIn
		err     error
	)

	if app := val(req.Body.App); app != "" {
		started, err = s.integrations.BeginAsUser(ctx, req.Integration, req.Body.Label, app)
	} else {
		started, err = s.integrations.Begin(ctx, req.Integration, req.Body.Label, val(req.Body.Services))
	}

	if err != nil {
		return nil, err
	}

	return openapi.BeginIntegrationSignIn200JSONResponse(toSignIn(started)), nil
}

func toSignIn(in integration.SignIn) openapi.SignIn {
	out := openapi.SignIn{
		Id: in.ID, Url: in.URL, UserCode: opt(in.UserCode), QrCode: opt(in.Code),
		ExpiresInSeconds: int(in.ExpiresIn.Seconds()),
	}
	if in.Code != "" && !in.CodeExpires.IsZero() {
		out.QrExpiresAt = &in.CodeExpires
	}

	return out
}

// GetIntegrationSignIn answers the current QR code of a linking in progress (issue #615).
func (s *Server) GetIntegrationSignIn(ctx context.Context, req openapi.GetIntegrationSignInRequestObject,
) (openapi.GetIntegrationSignInResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	current, err := s.integrations.Code(ctx, req.Integration, req.SignInId)
	if err != nil {
		return nil, err
	}

	return openapi.GetIntegrationSignIn200JSONResponse(toSignIn(current)), nil
}

// CompleteIntegrationSignIn finishes a sign-in with the address the browser
// ended on, or, for one by device code, by waiting until the owner entered
// it. The address holds the code, so it is not logged.
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

// PutIntegrationConnection connects an Integration of kind app under a
// label with the app's id and secret, or one of kind account when the body
// gives its address, user name and password. Neither secret is logged or in
// the answer.
func (s *Server) PutIntegrationConnection(ctx context.Context, req openapi.PutIntegrationConnectionRequestObject,
) (openapi.PutIntegrationConnectionResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	var (
		conn integration.Connection
		err  error
	)

	var b openapi.NewAccountConnection
	if req.Body != nil {
		b = *req.Body
	}

	switch {
	case val(b.RoleArn) != "" || val(b.Key) != "":
		conn, err = s.integrations.ConnectRole(ctx, req.Integration, req.Label, integration.Role{
			ARN: val(b.RoleArn), Key: val(b.Key), AccessKeyID: val(b.AccessKeyId),
			SecretAccessKey: val(b.SecretAccessKey),
		})
	case val(b.Address) != "" || val(b.Username) != "" || val(b.Password) != "":
		conn, err = s.integrations.ConnectAccount(ctx, req.Integration, req.Label, integration.Account{
			Address: val(b.Address), User: val(b.Username), Password: val(b.Password),
		})
	default:
		conn, err = s.integrations.Connect(ctx, req.Integration, req.Label, integration.App{
			ID: val(b.AppId), Secret: val(b.AppSecret),
		})
	}

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

// CheckIntegrationConnection reads again what a Connection's account can do: the owner's "Check again".
func (s *Server) CheckIntegrationConnection(ctx context.Context, req openapi.CheckIntegrationConnectionRequestObject,
) (openapi.CheckIntegrationConnectionResponseObject, error) {
	if s.integrations == nil {
		return nil, errNoIntegrations
	}

	conn, err := s.integrations.Check(ctx, req.Integration, req.Label)
	if err != nil {
		return nil, err
	}

	return openapi.CheckIntegrationConnection200JSONResponse(toIntegrationConnection(conn)), nil
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

// Messaging is what the API asks of the messaging accounts linked as devices (issue #631).
type Messaging interface {
	Chats(ctx context.Context, integration, label string, limit int) ([]messaging.Chat, error)
	Messages(ctx context.Context, integration, label string, q messaging.Query) ([]messaging.Message, error)
}

// WithMessaging sets the service that reads the messaging accounts' kept chats.
func (s *Server) WithMessaging(m Messaging) *Server {
	s.messaging = m

	return s
}

var errNoMessaging = fmt.Errorf("%w: this control plane keeps no messaging accounts", orchestration.ErrUnavailable)

// ListMessagingChats lists the chats kept of a messaging account, the latest first.
func (s *Server) ListMessagingChats(ctx context.Context, req openapi.ListMessagingChatsRequestObject,
) (openapi.ListMessagingChatsResponseObject, error) {
	if s.messaging == nil {
		return nil, errNoMessaging
	}

	chats, err := s.messaging.Chats(ctx, req.Integration, req.Label, val(req.Params.Limit))
	if err != nil {
		return nil, err
	}

	out := openapi.ListMessagingChats200JSONResponse{Data: make([]openapi.MessagingChat, 0, len(chats))}
	for _, c := range chats {
		out.Data = append(out.Data, openapi.MessagingChat{
			Id: c.ID, Kind: openapi.MessagingChatKind(c.Kind), Name: c.Name, LastMessageAt: c.LastMessageAt,
		})
	}

	return out, nil
}

// ListMessagingMessages lists the messages kept of a messaging account, the latest first.
func (s *Server) ListMessagingMessages(ctx context.Context, req openapi.ListMessagingMessagesRequestObject,
) (openapi.ListMessagingMessagesResponseObject, error) {
	if s.messaging == nil {
		return nil, errNoMessaging
	}

	q := messaging.Query{Chat: val(req.Params.Chat), Search: val(req.Params.Q), Limit: val(req.Params.Limit)}
	if req.Params.Before != nil {
		q.Before = *req.Params.Before
	}

	msgs, err := s.messaging.Messages(ctx, req.Integration, req.Label, q)
	if err != nil {
		return nil, err
	}

	out := openapi.ListMessagingMessages200JSONResponse{Data: make([]openapi.MessagingMessage, 0, len(msgs))}
	for _, m := range msgs {
		msg := openapi.MessagingMessage{
			Id: m.ID, Chat: m.Chat, ChatName: m.ChatName, Sender: m.Sender, SenderName: m.SenderName, At: m.At,
			FromMe: m.FromMe, Text: m.Text, QuotedId: opt(m.QuotedID), Caption: opt(m.Caption),
			FileName: opt(m.FileName), MimeType: opt(m.MimeType), Edited: m.Edited, Revoked: m.Revoked,
		}
		if m.MediaType != "" {
			kind := openapi.MessagingMessageMediaType(m.MediaType)
			msg.MediaType = &kind
		}

		out.Data = append(out.Data, msg)
	}

	return out, nil
}
