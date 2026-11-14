package clientapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// Images through the owner's subscriptions (issue #431): a Bot makes or edits
// one with `hypomnema image generate|edit`, as it searches the web with
// `hypomnema web search` (web.go), the call recorded on its chat's gateway
// key; and the Images page of Settings → Gateway lists the providers in
// order, with each one's switch, model, state and usage, as the Web search
// page lists its own (web_search.go).

// Images makes images. chatID is the chat of the Bot's pi that asks, empty
// for another token: the control plane's wiring records the call on that
// chat's gateway key.
type Images interface {
	Make(ctx context.Context, chatID string, req modelgateway.ImageRequest) (modelgateway.ImageResult, error)
}

// ImageSettings is what the Images page shows and changes.
type ImageSettings interface {
	ImageSettings(ctx context.Context) ([]modelgateway.ImageProviderSetting, error)
	ImageCounts(ctx context.Context, now time.Time) (map[string]modelgateway.ImageCount, error)
	ImageRests(ctx context.Context) (map[string]map[string]time.Time, error)
	SetImageProvider(ctx context.Context, provider string, on bool) error
	SetImageModel(ctx context.Context, provider, model string) error
	SetImageOrder(ctx context.Context, providers []string) error
}

// WithImages gives the API the gateway's images and their settings.
func (s *Server) WithImages(images Images, settings ImageSettings) *Server {
	s.images, s.imageSettings = images, settings

	return s
}

var errNoImages = fmt.Errorf("%w: this control plane makes no images", orchestration.ErrUnavailable)

// GenerateImages makes or edits images.
func (s *Server) GenerateImages(ctx context.Context, req openapi.GenerateImagesRequestObject,
) (openapi.GenerateImagesResponseObject, error) {
	if s.images == nil {
		return nil, errNoImages
	}

	b := req.Body
	in := modelgateway.ImageRequest{
		Prompt: b.Prompt, Model: val(b.Model), N: val(b.N), Size: val(b.Size), Quality: val(b.Quality),
		Background: val(b.Background), ImageSize: val(b.ImageSize),
	}

	if b.Provider != nil {
		in.Provider = string(*b.Provider)
	}

	for _, img := range val(b.Images) {
		in.Images = append(in.Images, modelgateway.ImageInput{MIME: val(img.MimeType), Data: img.Data})
	}

	res, err := s.images.Make(ctx, chatOfCaller(ctx), in)
	if err != nil {
		return nil, err
	}

	out := openapi.GenerateImages200JSONResponse{
		Provider: openapi.ImageProvider(res.Provider), Model: res.Model, Created: res.Created,
		Images: make([]openapi.GeneratedImage, 0, len(res.Images)),
	}

	for _, img := range res.Images {
		g := openapi.GeneratedImage{B64Json: img.B64JSON, MimeType: img.MIME}
		if img.RevisedPrompt != "" {
			g.RevisedPrompt = ptr(img.RevisedPrompt)
		}

		out.Images = append(out.Images, g)
	}

	if len(res.Errors) > 0 {
		failures := make([]openapi.WebSearchError, 0, len(res.Errors))
		for _, f := range res.Errors {
			failures = append(failures, openapi.WebSearchError{Provider: f.Provider, Message: f.Model + ": " + f.Message})
		}

		out.Errors = &failures
	}

	return out, nil
}

// GetImages answers the image providers.
func (s *Server) GetImages(ctx context.Context, _ openapi.GetImagesRequestObject,
) (openapi.GetImagesResponseObject, error) {
	out, err := s.imageProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetImages200JSONResponse(out), nil
}

// SetImageProvider switches a provider or sets its model, and answers the providers.
func (s *Server) SetImageProvider(ctx context.Context, req openapi.SetImageProviderRequestObject,
) (openapi.SetImageProviderResponseObject, error) {
	switch {
	case s.imageSettings == nil:
		return nil, errNoImages
	case req.Body.On == nil && req.Body.Model == nil:
		return nil, fmt.Errorf("%w: the change sets on, model or both", orchestration.ErrInvalid)
	}

	provider := string(req.Provider)

	if req.Body.On != nil {
		if err := s.imageSettings.SetImageProvider(ctx, provider, *req.Body.On); err != nil {
			return nil, gatewayKeyError(err)
		}
	}

	if req.Body.Model != nil {
		if err := s.imageSettings.SetImageModel(ctx, provider, *req.Body.Model); err != nil {
			return nil, gatewayKeyError(err)
		}
	}

	out, err := s.imageProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetImageProvider200JSONResponse(out), nil
}

// SetImageOrder keeps the providers' order and answers them.
func (s *Server) SetImageOrder(ctx context.Context, req openapi.SetImageOrderRequestObject,
) (openapi.SetImageOrderResponseObject, error) {
	if s.imageSettings == nil {
		return nil, errNoImages
	}

	order := make([]string, 0, len(req.Body.Providers))
	for _, p := range req.Body.Providers {
		order = append(order, string(p))
	}

	if err := s.imageSettings.SetImageOrder(ctx, order); err != nil {
		return nil, gatewayKeyError(err)
	}

	out, err := s.imageProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetImageOrder200JSONResponse(out), nil
}

// imageProviders are the providers with their state: one can make images
// with an account of its runtime that is in use and signed in here, as the
// gateway takes them (subscription.Service.Token), a Codex one on a paid
// plan (codex.FreePlan); it rests while each of those rests.
func (s *Server) imageProviders(ctx context.Context) (openapi.Images, error) {
	if s.imageSettings == nil {
		return openapi.Images{}, errNoImages
	}

	settings, err := s.imageSettings.ImageSettings(ctx)
	if err != nil {
		return openapi.Images{}, err
	}

	now := s.now()

	counts, err := s.imageSettings.ImageCounts(ctx, now)
	if err != nil {
		return openapi.Images{}, err
	}

	rests, err := s.imageSettings.ImageRests(ctx)
	if err != nil {
		return openapi.Images{}, err
	}

	accounts, err := s.listAccounts(ctx)
	if err != nil {
		return openapi.Images{}, err
	}

	usable := map[string][]string{}

	for _, a := range accounts {
		if a.Status != orchestration.AccountActive || a.State != subscription.StateActive {
			continue
		}

		if a.Runtime == modelgateway.ImageProviderCodex && codex.FreePlan(a.Plan) {
			continue
		}

		usable[a.Runtime] = append(usable[a.Runtime], a.ID)
	}

	out := openapi.Images{Providers: make([]openapi.ImageProviderLink, 0, len(settings))}

	for _, st := range settings {
		c := counts[st.Provider]
		link := openapi.ImageProviderLink{
			Provider: openapi.ImageProvider(st.Provider), On: st.On, Model: st.Model,
			DefaultModel: modelgateway.DefaultImageModel(st.Provider), ModelChosen: st.Chosen,
			SuggestedModels: modelgateway.SuggestedImageModels(st.Provider), Accounts: len(usable[st.Provider]),
			Usage: openapi.WebSearchUsage{
				Today: openapi.WebSearchTally{Answered: c.Today.Answered, Failed: c.Today.Failed},
				Month: openapi.WebSearchTally{Answered: c.Month.Answered, Failed: c.Month.Failed},
			},
		}

		var free time.Time

		resting := len(usable[st.Provider]) > 0

		for _, id := range usable[st.Provider] {
			until, ok := rests[st.Provider][id]
			if !ok || !until.After(now) {
				resting = false

				break
			}

			if free.IsZero() || until.Before(free) {
				free = until
			}
		}

		switch {
		case !st.On:
			link.State = openapi.ImageProviderLinkStateOff
		case len(usable[st.Provider]) == 0:
			link.State = openapi.ImageProviderLinkStateNotSetUp
		case resting:
			link.State, link.RestingUntil = openapi.ImageProviderLinkStateResting, &free
		default:
			link.State = openapi.ImageProviderLinkStateReady
		}

		out.Providers = append(out.Providers, link)
	}

	return out, nil
}

// imageCode is the code of an image request's failure; ok is false for any
// other error. A provider's refusal of the request itself is the client's to
// fix; a request no provider could serve is unavailable.
func imageCode(err error) (openapi.ErrorCode, bool) {
	var failed *modelgateway.ImageError

	switch {
	case errors.As(err, &failed):
		switch status := failed.Status(); {
		case status == http.StatusTooManyRequests:
			return openapi.RateLimited, true
		case status >= 400 && status < 500:
			return openapi.InvalidRequest, true
		default:
			return openapi.Unavailable, true
		}
	case errors.Is(err, modelgateway.ErrInvalid):
		return openapi.InvalidRequest, true
	case errors.Is(err, errNoImages):
		return openapi.Unavailable, true
	}

	return "", false
}

// imageWait is how long an image request every provider refused for a limit
// should wait, for its Retry-After.
func imageWait(err error) (string, bool) {
	var failed *modelgateway.ImageError
	if !errors.As(err, &failed) || failed.Status() != http.StatusTooManyRequests {
		return "", false
	}

	return retryAfter(failed.RetryAfter()), true
}
