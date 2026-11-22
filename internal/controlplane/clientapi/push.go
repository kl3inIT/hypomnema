package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Web Push (issue #1047): the owner's devices that take a notification when something waits on him, a list he
// removes from and tests, as Coder's (POST and DELETE /users/me/webpush/subscription, POST /users/me/webpush/test).
// Every operation is the owner's alone (scopeOwner): only his own devices subscribe (owner, 2026-11-21).

// Pusher is the Web Push sender (package webpush).
type Pusher interface {
	// PublicKey is the VAPID public key, made on the first read; orchestration.ErrUnavailable when the control
	// plane pushes nothing.
	PublicKey(ctx context.Context) (string, error)
	// Test sends a test push to one device; orchestration.ErrNotFound when the push service no longer knows it.
	Test(ctx context.Context, sub orchestration.PushSubscription) error
}

// WithPusher sets the Web Push sender.
func (s *Server) WithPusher(p Pusher) *Server {
	s.pusher = p

	return s
}

func (s *Server) pushSender() (Pusher, error) {
	if s.pusher == nil {
		return nil, fmt.Errorf("%w: this control plane sends no push notifications", orchestration.ErrUnavailable)
	}

	return s.pusher, nil
}

// ListPushSubscriptions lists the owner's devices with the key a browser subscribes with.
func (s *Server) ListPushSubscriptions(ctx context.Context, _ openapi.ListPushSubscriptionsRequestObject,
) (openapi.ListPushSubscriptionsResponseObject, error) {
	p, err := s.pushSender()
	if err != nil {
		return nil, err
	}

	key, err := p.PublicKey(ctx)
	if err != nil {
		return nil, err
	}

	subs, err := s.svc.ListPushSubscriptions(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListPushSubscriptions200JSONResponse{
		PublicKey: key, Data: make([]openapi.PushSubscription, 0, len(subs)),
	}
	for _, sub := range subs {
		out.Data = append(out.Data, toPushSubscription(sub))
	}

	return out, nil
}

// PutPushSubscription keeps this browser's subscription.
func (s *Server) PutPushSubscription(ctx context.Context, req openapi.PutPushSubscriptionRequestObject,
) (openapi.PutPushSubscriptionResponseObject, error) {
	if _, err := s.pushSender(); err != nil {
		return nil, err
	}

	b := req.Body

	sub, err := s.svc.SavePushSubscription(ctx, orchestration.NewPushSubscription{
		Endpoint: b.Endpoint, P256dh: b.Keys.P256dh, Auth: b.Keys.Auth, Label: val(b.Label),
	})
	if err != nil {
		return nil, err
	}

	return openapi.PutPushSubscription200JSONResponse(toPushSubscription(sub)), nil
}

// DeletePushSubscription stops pushing to one device.
func (s *Server) DeletePushSubscription(ctx context.Context, req openapi.DeletePushSubscriptionRequestObject,
) (openapi.DeletePushSubscriptionResponseObject, error) {
	if err := s.svc.DeletePushSubscription(ctx, req.SubscriptionId); err != nil {
		return nil, err
	}

	return openapi.DeletePushSubscription204Response{}, nil
}

// TestPushSubscription sends a test notification to one device.
func (s *Server) TestPushSubscription(ctx context.Context, req openapi.TestPushSubscriptionRequestObject,
) (openapi.TestPushSubscriptionResponseObject, error) {
	p, err := s.pushSender()
	if err != nil {
		return nil, err
	}

	sub, err := s.svc.GetPushSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return nil, err
	}

	if err := p.Test(ctx, sub); err != nil {
		return nil, err
	}

	return openapi.TestPushSubscription204Response{}, nil
}

func toPushSubscription(sub orchestration.PushSubscription) openapi.PushSubscription {
	return openapi.PushSubscription{
		Id: sub.ID, Endpoint: sub.Endpoint, Label: sub.Label, CreatedAt: sub.CreatedAt, UpdatedAt: sub.UpdatedAt,
	}
}
