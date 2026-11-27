package clientapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The Muse page of Settings → Gateway (issue #782): the owner's Muse
// accounts in his order, each with its state, cause and usage, the tools'
// switch, and the sign-in made in his own tab of the shared browser, as the
// Images page lists its providers (images.go). Only the owner sees or changes
// it; agents ask the pool through their tools (agentapi/muse.go).

// Muse is the pool as the page uses it.
type Muse interface {
	Accounts(ctx context.Context) ([]muse.Account, error)
	ToolsOn(ctx context.Context) (bool, error)
	SetToolsOn(ctx context.Context, on bool) error
	SetOn(ctx context.Context, id string, on bool) error
	Move(ctx context.Context, id string, up bool) error
	Remove(ctx context.Context, id string) error
	SignInStart(ctx context.Context) error
	SignInCheck(ctx context.Context, id, label string) (muse.SignIn, error)
}

// WithMuse gives the API the Muse pool.
func (s *Server) WithMuse(m Muse) *Server {
	s.muse = m

	return s
}

var errNoMuse = fmt.Errorf("%w: this control plane has no Muse (it needs the shared browser and the vault's keys)",
	orchestration.ErrUnavailable)

// museErr says the pool's errors as the API's.
func museErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, muse.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	case errors.Is(err, muse.ErrUnavailable):
		return fmt.Errorf("%w: %w", orchestration.ErrUnavailable, err)
	}

	return err
}

func (s *Server) musePool(ctx context.Context) (openapi.Muse, error) {
	if s.muse == nil {
		return openapi.Muse{}, errNoMuse
	}

	on, err := s.muse.ToolsOn(ctx)
	if err != nil {
		return openapi.Muse{}, err
	}

	accounts, err := s.muse.Accounts(ctx)
	if err != nil {
		return openapi.Muse{}, err
	}

	out := openapi.Muse{ToolsOn: on, Accounts: make([]openapi.MuseAccount, 0, len(accounts))}
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, toMuseAccount(a))
	}

	return out, nil
}

func toMuseAccount(a muse.Account) openapi.MuseAccount {
	out := openapi.MuseAccount{
		Id: a.ID, Label: a.Label, Position: a.Position, On: a.On, State: a.State,
		Resting: a.Resting, Usage: a.Usage,
	}

	if a.Cause != "" {
		out.Cause = ptr(a.Cause)
	}

	if a.UsageError != "" {
		out.UsageError = ptr(a.UsageError)
	}

	if !a.UsageReadAt.IsZero() {
		out.UsageReadAt = ptr(a.UsageReadAt)
	}

	if !a.CheckedAt.IsZero() {
		out.CheckedAt = ptr(a.CheckedAt)
	}

	return out
}

// GetMuse answers the pool.
func (s *Server) GetMuse(ctx context.Context, _ openapi.GetMuseRequestObject) (openapi.GetMuseResponseObject, error) {
	out, err := s.musePool(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetMuse200JSONResponse(out), nil
}

// SetMuseTools switches the Muse tools.
func (s *Server) SetMuseTools(ctx context.Context, req openapi.SetMuseToolsRequestObject,
) (openapi.SetMuseToolsResponseObject, error) {
	if s.muse == nil {
		return nil, errNoMuse
	}

	if err := s.muse.SetToolsOn(ctx, req.Body.On); err != nil {
		return nil, museErr(err)
	}

	out, err := s.musePool(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetMuseTools200JSONResponse(out), nil
}

// StartMuseSignIn opens muse.ai in the owner's tab.
func (s *Server) StartMuseSignIn(ctx context.Context, _ openapi.StartMuseSignInRequestObject,
) (openapi.StartMuseSignInResponseObject, error) {
	if s.muse == nil {
		return nil, errNoMuse
	}

	if err := s.muse.SignInStart(ctx); err != nil {
		return nil, museErr(err)
	}

	return openapi.StartMuseSignIn204Response{}, nil
}

// CheckMuseSignIn looks for a finished sign-in.
func (s *Server) CheckMuseSignIn(ctx context.Context, req openapi.CheckMuseSignInRequestObject,
) (openapi.CheckMuseSignInResponseObject, error) {
	if s.muse == nil {
		return nil, errNoMuse
	}

	var id, label string
	if req.Body != nil {
		id, label = val(req.Body.Account), val(req.Body.Label)
	}

	in, err := s.muse.SignInCheck(ctx, id, label)
	if err != nil {
		return nil, museErr(err)
	}

	out := openapi.CheckMuseSignIn200JSONResponse{Done: in.Done}
	if in.Waiting != "" {
		out.Waiting = ptr(in.Waiting)
	}

	if in.Account != nil {
		a := toMuseAccount(*in.Account)
		out.Account = &a
	}

	return out, nil
}

// SetMuseAccount puts an account in use or out of it.
func (s *Server) SetMuseAccount(ctx context.Context, req openapi.SetMuseAccountRequestObject,
) (openapi.SetMuseAccountResponseObject, error) {
	if s.muse == nil {
		return nil, errNoMuse
	}

	if err := s.muse.SetOn(ctx, req.AccountId, req.Body.On); err != nil {
		return nil, museErr(err)
	}

	out, err := s.musePool(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetMuseAccount200JSONResponse(out), nil
}

// RemoveMuseAccount removes an account and its cookies.
func (s *Server) RemoveMuseAccount(ctx context.Context, req openapi.RemoveMuseAccountRequestObject,
) (openapi.RemoveMuseAccountResponseObject, error) {
	if s.muse == nil {
		return nil, errNoMuse
	}

	if err := s.muse.Remove(ctx, req.AccountId); err != nil {
		return nil, museErr(err)
	}

	out, err := s.musePool(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.RemoveMuseAccount200JSONResponse(out), nil
}

// MoveMuseAccount moves an account one place.
func (s *Server) MoveMuseAccount(ctx context.Context, req openapi.MoveMuseAccountRequestObject,
) (openapi.MoveMuseAccountResponseObject, error) {
	if s.muse == nil {
		return nil, errNoMuse
	}

	if err := s.muse.Move(ctx, req.AccountId, req.Body.Direction == openapi.Up); err != nil {
		return nil, museErr(err)
	}

	out, err := s.musePool(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.MoveMuseAccount200JSONResponse(out), nil
}
