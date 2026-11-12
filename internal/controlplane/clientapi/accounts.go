package clientapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// The accounts and the gateway's models, for the Models page (issue #199).
// The references that keep subscription accounts show an account under its
// provider with its state: 9router (providers/[id]/ConnectionRow.js),
// OpenClaw (pages/model-providers), Orca (settings/accounts).

// ListAccounts returns the accounts workers and the gateway run under, with
// the state of each sign-in made on the control plane. No secret is in it.
func (s *Server) ListAccounts(ctx context.Context, _ openapi.ListAccountsRequestObject,
) (openapi.ListAccountsResponseObject, error) {
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListAccounts200JSONResponse{Data: make([]openapi.Account, 0, len(accounts))}
	for _, a := range accounts {
		out.Data = append(out.Data, toAccount(a))
	}

	return out, nil
}

func (s *Server) listAccounts(ctx context.Context) ([]subscription.Account, error) {
	if s.accounts != nil {
		return s.accounts.List(ctx)
	}

	plain, err := s.svc.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]subscription.Account, 0, len(plain))
	for _, a := range plain {
		out = append(out, subscription.Account{Account: a})
	}

	return out, nil
}

// account returns one account as the API shows it.
func (s *Server) account(ctx context.Context, id string) (openapi.Account, error) {
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		return openapi.Account{}, err
	}

	for _, a := range accounts {
		if a.ID == id {
			return toAccount(a), nil
		}
	}

	return openapi.Account{}, fmt.Errorf("%w: account %s", orchestration.ErrNotFound, id)
}

// EnableAccount puts an account back in use.
func (s *Server) EnableAccount(ctx context.Context, req openapi.EnableAccountRequestObject,
) (openapi.EnableAccountResponseObject, error) {
	if err := s.svc.EnableAccount(ctx, req.AccountId); err != nil {
		return nil, err
	}

	a, err := s.account(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}

	return openapi.EnableAccount200JSONResponse(a), nil
}

// DisableAccount takes an account out of use: placement and the gateway pass
// it over, and its sign-in and its record stay (9router's active toggle).
func (s *Server) DisableAccount(ctx context.Context, req openapi.DisableAccountRequestObject,
) (openapi.DisableAccountResponseObject, error) {
	if err := s.svc.DisableAccount(ctx, req.AccountId); err != nil {
		return nil, err
	}

	a, err := s.account(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}

	return openapi.DisableAccount200JSONResponse(a), nil
}

// RemoveAccount deletes an account that was signed in on the control plane,
// with its tokens. One whose CLI was signed in on a node has no tokens here
// and is refused: its login is on that node.
func (s *Server) RemoveAccount(ctx context.Context, req openapi.RemoveAccountRequestObject,
) (openapi.RemoveAccountResponseObject, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("%w: accounts are not signed in on this control plane", orchestration.ErrUnavailable)
	}

	if err := s.accounts.Remove(ctx, req.AccountId); err != nil {
		if errors.Is(err, subscription.ErrNoTokens) {
			return nil, fmt.Errorf("%w: %w", orchestration.ErrConflict, err)
		}

		return nil, err
	}

	return openapi.RemoveAccount204Response{}, nil
}

// GetAccountLimits returns how much of an account's limits is used (issue
// #199, decisions 7 and 8). The reading and its cache are the subscription
// service's. An account with nothing to show is no error: the page shows the
// account without its bars, as 9router's and Orca's do, and says why.
func (s *Server) GetAccountLimits(ctx context.Context, req openapi.GetAccountLimitsRequestObject,
) (openapi.GetAccountLimitsResponseObject, error) {
	out := openapi.AccountLimits{Windows: []openapi.AccountLimitWindow{}}

	if s.accounts == nil {
		why := openapi.AccountLimitsUnavailableNotGiven
		out.Unavailable = &why

		return openapi.GetAccountLimits200JSONResponse(out), nil
	}

	limits, err := s.accounts.Limits(ctx, req.AccountId, val(req.Params.Fresh))
	if err != nil {
		if errors.Is(err, orchestration.ErrNotFound) {
			return nil, err
		}

		why, detail := limitsUnavailable(err)
		out.Unavailable = &why

		if detail != "" {
			out.Detail = &detail
		}

		return openapi.GetAccountLimits200JSONResponse(out), nil
	}

	return openapi.GetAccountLimits200JSONResponse(toLimits(limits)), nil
}

// RedeemAccountLimitReset uses one of an account's limit resets (issue #199,
// decision 17). It cannot be taken back, so the page asks first; the request
// id is the page's, and a request sent twice uses one reset.
func (s *Server) RedeemAccountLimitReset(ctx context.Context, req openapi.RedeemAccountLimitResetRequestObject,
) (openapi.RedeemAccountLimitResetResponseObject, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("%w: accounts are not signed in on this control plane", orchestration.ErrUnavailable)
	}

	limits, err := s.accounts.UseReset(ctx, req.AccountId, req.Body.RequestId)

	switch {
	case err == nil:
		return openapi.RedeemAccountLimitReset200JSONResponse(toLimits(limits)), nil
	case errors.Is(err, orchestration.ErrNotFound):
		return nil, err
	case errors.Is(err, subscription.ErrBadResetRequest):
		return nil, fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	case errors.Is(err, subscription.ErrNoResets), errors.Is(err, subscription.ErrNothingToReset),
		errors.Is(err, subscription.ErrResetWait), errors.Is(err, subscription.ErrNoTokens),
		errors.Is(err, subscription.ErrNoLimits):
		return nil, fmt.Errorf("%w: %w", orchestration.ErrConflict, err)
	default:
		return nil, fmt.Errorf("%w: %w", orchestration.ErrUnavailable, err)
	}
}

// toLimits is a reading as the API shows it.
func toLimits(limits subscription.Limits) openapi.AccountLimits {
	out := openapi.AccountLimits{Windows: []openapi.AccountLimitWindow{}}

	for _, w := range limits.Windows {
		window := openapi.AccountLimitWindow{Name: w.Name, UsedPercent: float32(w.UsedPercent)}
		if !w.ResetsAt.IsZero() {
			window.ResetsAt = &w.ResetsAt
		}

		out.Windows = append(out.Windows, window)
	}

	if !limits.ReadAt.IsZero() {
		out.ReadAt = &limits.ReadAt
	}

	if limits.Resets.Available > 0 {
		resets := openapi.AccountLimitResets{Available: limits.Resets.Available}
		if !limits.Resets.ExpiresAt.IsZero() {
			resets.ExpiresAt = &limits.Resets.ExpiresAt
		}

		out.Resets = &resets
	}

	return out
}

// limitsUnavailable says why an account has no limits to show.
func limitsUnavailable(err error) (openapi.AccountLimitsUnavailable, string) {
	switch {
	case errors.Is(err, subscription.ErrNoLimits):
		return openapi.AccountLimitsUnavailableNotGiven, ""
	case errors.Is(err, subscription.ErrNoTokens):
		return openapi.AccountLimitsUnavailableSignIn, ""
	case errors.Is(err, subscription.ErrLimitsResting):
		return openapi.AccountLimitsUnavailableResting, ""
	default:
		return openapi.AccountLimitsUnavailableFailed, err.Error()
	}
}

// ListGatewayModels returns the models the gateway serves and where each is
// served from, in the order its targets are tried.
func (s *Server) ListGatewayModels(ctx context.Context, _ openapi.ListGatewayModelsRequestObject,
) (openapi.ListGatewayModelsResponseObject, error) {
	out := openapi.ListGatewayModels200JSONResponse{Data: []openapi.GatewayModel{}}
	if s.gatewayModels == nil {
		return out, nil
	}

	models, err := s.gatewayModels.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	for _, m := range models {
		out.Data = append(out.Data, toGatewayModel(m))
	}

	return out, nil
}

func toGatewayModel(m modelgateway.Model) openapi.GatewayModel {
	out := openapi.GatewayModel{Name: m.Name, UpdatedAt: m.UpdatedAt, Targets: []openapi.GatewayModelTarget{}}

	for _, t := range m.Targets {
		out.Targets = append(out.Targets, openapi.GatewayModelTarget{
			Provider: t.Provider, UpstreamModel: t.UpstreamModel,
		})
	}

	return out
}

func toAccount(a subscription.Account) openapi.Account {
	out := openapi.Account{
		Id: a.ID, Runtime: a.Runtime, Label: a.Label, Status: openapi.AccountStatus(a.Status),
		Position: a.Position, CoolingUntil: a.CoolingUntil, TokenExpiresAt: a.AccessTokenExpiresAt,
	}

	if a.CoolingUntil != nil && a.CoolingReason != "" {
		out.CoolingReason = &a.CoolingReason
	}

	if a.Email != "" {
		out.Email = &a.Email
	}

	if a.Plan != "" {
		out.Plan = &a.Plan
	}

	signIns := map[subscription.State]openapi.AccountSignIn{
		subscription.StateActive:        openapi.AccountSignInActive,
		subscription.StateLoginRequired: openapi.AccountSignInLoginRequired,
		subscription.StateNoTokens:      openapi.AccountSignInNoTokens,
	}
	if signIn, ok := signIns[a.State]; ok {
		out.SignIn = &signIn
	}

	return out
}
