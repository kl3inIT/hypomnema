package modelgateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/cloudcode"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/antigravity"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
)

// Images made and edited through the owner's subscriptions (issue #431): a
// Codex account through the Codex backend's Images routes, an Antigravity
// account through Cloud Code's generateContent with requestType image_gen;
// Claude makes no images. The providers are asked in the owner's order (by
// default Codex, then Antigravity), each that is on, with the model the
// request names or the one the owner set for it; the first that answers
// gives the images, and the answer says why those before it did not, as a
// web search does (websearch, omp's fallbackNotes). What each asks and reads
// is its package's (codex/images.go, antigravity/images.go).
//
// Under one provider the accounts are taken in turn as chat takes them
// (underAccounts): the next one on 401, 402, 403, 429 and the 5xx of an
// overload. A refusal rests that account's image model, never its chat:
// until the reset the vendor names, else as long as 9router-go waits after
// the kind of failure (accounts.go's numbers), as 9router-go locks only the
// image model of the connection (handlers/media/antigravity_image.go
// LockConnectionModel, geminiQuotaResetSeconds) and OmniRoute keeps an image's
// failures off the chat account. An answer clears that rest only.

// The providers an image may be made by.
const (
	ImageProviderCodex       = "codex"
	ImageProviderAntigravity = "antigravity"
)

// ImageProviders are the image providers in their default order (owner,
// 2026-11-14: Codex, then Antigravity).
func ImageProviders() []string { return []string{ImageProviderCodex, ImageProviderAntigravity} }

// DefaultImageModel is the model a provider makes images with when neither
// the request nor the owner names one.
func DefaultImageModel(provider string) string {
	switch provider {
	case ImageProviderCodex:
		return codex.ImageModel
	case ImageProviderAntigravity:
		return antigravity.ImageModel
	default:
		return ""
	}
}

// SuggestedImageModels are the models offered for a provider on the Images
// page: those the references name in code (codex-rs gpt-image-2; OmniRoute's
// gemini-3.1-flash-image and its gemini-3-pro-image alias). Any other name
// is passed on as it is, so a model released later can be tried (owner,
// 2026-11-14).
func SuggestedImageModels(provider string) []string {
	switch provider {
	case ImageProviderCodex:
		return []string{codex.ImageModel}
	case ImageProviderAntigravity:
		return []string{antigravity.ImageModel, "gemini-3-pro-image"}
	default:
		return nil
	}
}

const (
	// ModelImagePrefix starts the model an image call is recorded under:
	// image/<the upstream model>.
	ModelImagePrefix = "image/"
	// maxImages is how many images one request may ask for: OmniRoute's
	// MAX_CODEX_IMAGES_PER_REQUEST, OpenAI's own bound of n.
	maxImages = 10
	// maxImageInput bounds one reference image (opencode's image tool,
	// MAX_INPUT_BYTES).
	maxImageInput = 20 << 20
	// MaxImageRequestBytes bounds an image request's body: references are
	// sent whole, base64 or in a form.
	MaxImageRequestBytes = 64 << 20
	// maxImageAnswer bounds one answer: a few images in base64.
	maxImageAnswer = 96 << 20
	// imageHeaderTimeout is how long an image takes to come: an Images call
	// answers once the image is made (Hermes waits 300 s).
	imageHeaderTimeout = 5 * time.Minute
)

// imageModelPattern is a model's name as a provider takes it.
var imageModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)

// ValidImageModel reports whether a name can be passed to a provider.
func ValidImageModel(model string) bool { return imageModelPattern.MatchString(model) }

// ImageInput is a reference image of an edit.
type ImageInput struct {
	MIME string
	Data []byte
}

// ImageRequest is what a client asks for.
type ImageRequest struct {
	Prompt string
	// Model is "" or "auto" (each provider's own), <provider>/<model>, or a
	// model whose name says its provider: gpt-image-* is Codex's, gemini-*
	// Antigravity's (OmniRoute's isCodexImagesApiModel).
	Model string
	// Provider, when set, is the only provider asked.
	Provider string
	// N is how many images, 1 to 10.
	N int
	// Size is OpenAI's (1024x1536, auto) or an aspect ratio; AspectRatio and
	// ImageSize (1K, 2K, 4K) are Antigravity's, as OmniRoute takes them.
	Size, AspectRatio, ImageSize string
	Quality, Background          string
	Images                       []ImageInput
}

// GeneratedImage is one image made: its bytes in base64.
type GeneratedImage struct {
	B64JSON       string
	MIME          string
	RevisedPrompt string
}

// ImageFailure is why a provider did not make the images.
type ImageFailure struct {
	Provider, Model string
	// Status is the HTTP status the failure stands for; RetryAfter the wait
	// it asked for, with a 429.
	Status     int
	RetryAfter time.Duration
	Message    string
}

// ImageResult is the images one provider made.
type ImageResult struct {
	Provider, Model string
	Created         int64
	Images          []GeneratedImage
	Tokens          Tokens
	// AccountID is the account of the last call.
	AccountID string
	// Errors are the providers tried before, and why each did not answer.
	Errors []ImageFailure
}

// ImageError says no provider made the images: each one's failure, in turn.
type ImageError struct{ Failures []ImageFailure }

func (e *ImageError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, f.Provider+" "+f.Model+": "+f.Message)
	}

	return "no image provider made the image: " + strings.Join(parts, "; ")
}

// Status is the HTTP status of the whole: 429 when every provider is limited,
// 503 when none had an account to use, a lone provider's own refusal of the
// request (a 4xx), else 502.
func (e *ImageError) Status() int {
	limited, missing := true, true

	for _, f := range e.Failures {
		limited = limited && f.Status == http.StatusTooManyRequests
		missing = missing && f.Status == http.StatusServiceUnavailable
	}

	switch {
	case len(e.Failures) == 0:
		return http.StatusServiceUnavailable
	case limited:
		return http.StatusTooManyRequests
	case missing:
		return http.StatusServiceUnavailable
	case len(e.Failures) == 1 && e.Failures[0].Status >= 400 && e.Failures[0].Status < 500:
		return e.Failures[0].Status
	default:
		return http.StatusBadGateway
	}
}

// RetryAfter is the shortest wait the limited providers asked for.
func (e *ImageError) RetryAfter() time.Duration {
	var wait time.Duration

	for _, f := range e.Failures {
		if f.RetryAfter > 0 && (wait == 0 || f.RetryAfter < wait) {
			wait = f.RetryAfter
		}
	}

	return wait
}

// imageTarget is a provider asked, with its model.
type imageTarget struct{ provider, model string }

// imageTargets are the providers a request asks, in turn, with each one's model.
func imageTargets(req ImageRequest, settings []ImageProviderSetting) ([]imageTarget, error) {
	byProvider := map[string]ImageProviderSetting{}
	for _, s := range settings {
		byProvider[s.Provider] = s
	}

	provider, model := strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model)

	if p, m, ok := strings.Cut(model, "/"); ok && slices.Contains(ImageProviders(), p) {
		if provider != "" && provider != p {
			return nil, fmt.Errorf("%w: model %q is %s's, not %s's", ErrInvalid, model, p, provider)
		}

		provider, model = p, m
	}

	if model == "auto" {
		model = ""
	}

	if provider == "" && model != "" {
		switch {
		case strings.HasPrefix(model, "gpt-image"):
			provider = ImageProviderCodex
		case strings.HasPrefix(model, "gemini-"):
			provider = ImageProviderAntigravity
		default:
			return nil, fmt.Errorf("%w: model %q names no provider: write it codex/<model> or antigravity/<model>, "+
				"or leave it out", ErrInvalid, model)
		}
	}

	if model != "" && !ValidImageModel(model) {
		return nil, fmt.Errorf("%w: %q is not a model's name", ErrInvalid, model)
	}

	if provider != "" {
		s, ok := byProvider[provider]

		switch {
		case !ok:
			return nil, fmt.Errorf("%w: no image provider %q: %s", ErrInvalid, provider,
				strings.Join(ImageProviders(), ", "))
		case !s.On:
			return nil, fmt.Errorf("%w: image provider %s is off", ErrInvalid, provider)
		case model == "":
			model = s.Model
		}

		return []imageTarget{{provider: provider, model: model}}, nil
	}

	var out []imageTarget

	for _, s := range settings {
		if s.On {
			out = append(out, imageTarget{provider: s.Provider, model: s.Model})
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("%w: every image provider is off", ErrInvalid)
	}

	return out, nil
}

// checkImageRequest refuses what no provider could make, before any is asked.
func checkImageRequest(req *ImageRequest) error {
	req.Prompt = strings.TrimSpace(req.Prompt)

	if req.N == 0 {
		req.N = 1
	}

	switch {
	case req.Prompt == "":
		return fmt.Errorf("%w: the prompt is empty", ErrInvalid)
	case req.N < 1 || req.N > maxImages:
		return fmt.Errorf("%w: n is 1 to %d", ErrInvalid, maxImages)
	}

	for i := range req.Images {
		img := &req.Images[i]

		if len(img.Data) == 0 {
			return fmt.Errorf("%w: reference image %d is empty", ErrInvalid, i+1)
		}

		if len(img.Data) > maxImageInput {
			return fmt.Errorf("%w: reference image %d is larger than %d MiB", ErrInvalid, i+1, maxImageInput>>20)
		}

		if img.MIME == "" || img.MIME == "application/octet-stream" {
			img.MIME = http.DetectContentType(img.Data)
		}

		if !strings.HasPrefix(img.MIME, "image/") {
			return fmt.Errorf("%w: reference image %d is not an image (%s)", ErrInvalid, i+1, img.MIME)
		}
	}

	return nil
}

// MakeImages makes the images a request asks for, from the first provider
// that answers.
func (s *Server) MakeImages(ctx context.Context, req ImageRequest) (ImageResult, error) {
	if err := checkImageRequest(&req); err != nil {
		return ImageResult{}, err
	}

	settings, err := s.svc.ImageSettings(ctx)
	if err != nil {
		return ImageResult{}, err
	}

	targets, err := imageTargets(req, settings)
	if err != nil {
		return ImageResult{}, err
	}

	var failures []ImageFailure

	for _, t := range targets {
		var (
			res     ImageResult
			failure *ImageFailure
		)

		switch t.provider {
		case ImageProviderCodex:
			res, failure = s.codexImages(ctx, t.model, req)
		case ImageProviderAntigravity:
			res, failure = s.antigravityImages(ctx, t.model, req)
		}

		if failure == nil {
			res.Provider, res.Model, res.Errors = t.provider, t.model, failures

			return res, nil
		}

		if ctx.Err() != nil {
			return ImageResult{}, ctx.Err()
		}

		failure.Provider, failure.Model = t.provider, t.model
		failures = append(failures, *failure)
	}

	return ImageResult{}, &ImageError{Failures: failures}
}

// imageRests are the image models that rest, by provider, account and model,
// in memory as the references keep theirs; and how many times in a row each
// was limited, for the doubling wait.
type imageRests struct {
	mu     sync.Mutex
	until  map[string]time.Time
	levels map[string]int
}

func newImageRests() *imageRests {
	return &imageRests{until: map[string]time.Time{}, levels: map[string]int{}}
}

func restKey(provider, account, model string) string {
	return provider + "\x00" + account + "\x00" + model
}

// resting is when an image model of an account is free again, if it rests.
func (r *imageRests) resting(key string, now time.Time) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	until := r.until[key]

	return until, until.After(now)
}

// rest keeps an image model of an account out of use after a refusal, for as
// long as accounts.go's cool would keep the account: the vendor's reset (at
// most two hours), an hour for a quota used up, a wait doubling from 2 s to
// 5 minutes after a rate limit or an overload, 2 minutes after a refusal of
// the account. Another failure rests nothing. It returns the wait.
func (r *imageRests) rest(key string, now time.Time, status int, quotaUsedUp bool, reset time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	var wait time.Duration

	switch {
	case reset.After(now):
		wait = min(reset.Sub(now), maxProviderReset)
	case quotaUsedUp:
		wait = quotaCooldown
	case status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		level := min(r.levels[key]+1, backoffLevels)
		r.levels[key] = level
		wait = min(backoffFirst<<(level-1), backoffMax)
	case status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden:
		wait = refusedCooldown
	default:
		return 0
	}

	r.until[key] = now.Add(wait)

	return wait
}

// served forgets a model's rest and its failures in a row.
func (r *imageRests) served(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.until, key)
	delete(r.levels, key)
}

// ImageRests are the accounts whose image model rests now, by provider and
// account, with when each is free: for the Images page.
func (s *Server) ImageRests(ctx context.Context) (map[string]map[string]time.Time, error) {
	settings, err := s.svc.ImageSettings(ctx)
	if err != nil {
		return nil, err
	}

	now := s.svc.clock.Now()
	models := map[string]string{}

	for _, st := range settings {
		models[st.Provider] = st.Model
	}

	r := s.imageRests
	r.mu.Lock()
	defer r.mu.Unlock()

	out := map[string]map[string]time.Time{}

	for key, until := range r.until {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) != 3 || !until.After(now) || models[parts[0]] != parts[2] {
			continue
		}

		if out[parts[0]] == nil {
			out[parts[0]] = map[string]time.Time{}
		}

		out[parts[0]][parts[1]] = until
	}

	return out, nil
}

// imageRefusal is a refusal of one account in OpenAI's shape, for
// underAccounts to move on by its status.
func imageRefusal(status int, code, message string, wait time.Duration) *http.Response {
	kind := typeServer

	switch {
	case status == http.StatusTooManyRequests:
		kind = typeRateLimit
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired:
		kind = typePermission
	case status < http.StatusInternalServerError:
		kind = typeInvalidRequest
	}

	return openAIError(status, kind, code, message, wait)
}

// failureOf is what underAccounts ended with, as a provider's failure.
func (s *Server) failureOf(resp *http.Response, err error, noAccount string) *ImageFailure {
	switch {
	case errors.Is(err, ErrNoAccount):
		return &ImageFailure{Status: http.StatusServiceUnavailable, Message: noAccount}
	case err != nil:
		return &ImageFailure{Status: http.StatusBadGateway, Message: err.Error()}
	}

	defer resp.Body.Close() //nolint:errcheck // Made here or read; closing adds nothing.

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	f := &ImageFailure{Status: resp.StatusCode, Message: errorText(body)}

	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		f.RetryAfter = time.Duration(secs) * time.Second
	}

	if f.Status == imageRefusedStatus {
		// The model answered without an image: not an account's failure, and not the client's request.
		f.Status = http.StatusBadGateway
	}

	return f
}

// imageRefusedStatus marks, inside a provider's turn, an answer that held no
// image: no other account is asked for it, as OmniRoute fails such an answer
// outright.
const imageRefusedStatus = http.StatusUnprocessableEntity

// readImageAnswer reads an image answer, within maxImageAnswer.
func readImageAnswer(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImageAnswer+1))
	if err != nil {
		return nil, fmt.Errorf("reading the images: %w", err)
	}

	if len(raw) > maxImageAnswer {
		return nil, fmt.Errorf("the answer is larger than %d MiB", maxImageAnswer>>20)
	}

	return raw, nil
}

// codexImages makes the images under the Codex accounts: one call an image,
// all at once, as OmniRoute fans n out (Promise.all); one that fails fails
// the whole, as there.
func (s *Server) codexImages(ctx context.Context, model string, req ImageRequest) (ImageResult, *ImageFailure) {
	if s.accounts == nil {
		return ImageResult{}, &ImageFailure{Status: http.StatusServiceUnavailable, Message: "no Codex account"}
	}

	opts := codex.ImageOptions{Model: model, Size: req.Size, Quality: req.Quality, Background: req.Background}
	for _, img := range req.Images {
		opts.Images = append(opts.Images, codex.ImageInput{MIME: img.MIME, Data: img.Data})
	}

	type outcome struct {
		answer  codex.ImageAnswer
		account string
		failure *ImageFailure
	}

	outcomes := make([]outcome, req.N)

	var wg sync.WaitGroup

	for i := range req.N {
		wg.Go(func() {
			answer, account, failure := s.codexImage(ctx, model, req.Prompt, opts)
			outcomes[i] = outcome{answer: answer, account: account, failure: failure}
		})
	}

	wg.Wait()

	var res ImageResult

	for _, o := range outcomes {
		if o.failure != nil {
			return ImageResult{}, o.failure
		}

		mime := "image/" + cmpOr(o.answer.Format, "png")
		if o.answer.Format == "jpeg" || o.answer.Format == "jpg" {
			mime = "image/jpeg"
		}

		for _, b64 := range o.answer.Images {
			res.Images = append(res.Images, GeneratedImage{B64JSON: b64, MIME: mime})
		}

		if res.Created == 0 {
			res.Created = o.answer.Created
		}

		res.Tokens.Prompt += o.answer.Usage.Input
		res.Tokens.Completion += o.answer.Usage.Output
		res.AccountID = o.account
	}

	return res, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

// codexImage makes one image under the Codex accounts in turn.
func (s *Server) codexImage(
	ctx context.Context, model, prompt string, opts codex.ImageOptions,
) (codex.ImageAnswer, string, *ImageFailure) {
	var (
		answer  codex.ImageAnswer
		account string
	)

	body := codex.ImageBody(prompt, opts)

	resp, err := s.underAccounts(ctx, codexRuntime, "", nil, func(token AccountToken) (*http.Response, error) {
		now := s.svc.clock.Now()
		key := restKey(ImageProviderCodex, token.AccountID, model)

		if codex.FreePlan(token.Plan) {
			return imageRefusal(http.StatusForbidden, "plan_not_entitled",
				"the ChatGPT account is on the free plan, which makes no images", 0), nil
		}

		if until, resting := s.imageRests.resting(key, now); resting {
			return imageRefusal(http.StatusTooManyRequests, "image_rate_limited",
				"the account rests after a limit", until.Sub(now)), nil
		}

		resp, err := s.codexImagePost(ctx, &token, codex.ImagePath(opts), body)
		if err != nil {
			return nil, err
		}

		status, retry := resp.StatusCode, resp.Header.Get("Retry-After")

		raw, err := readImageAnswer(resp)
		if err != nil {
			return nil, err
		}

		if status == http.StatusOK {
			got, err := codex.ParseImages(raw)
			if err != nil {
				return imageRefusal(http.StatusBadGateway, "upstream_failed", err.Error(), 0), nil
			}

			s.imageRests.served(key)

			answer, account = got, token.AccountID

			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}

		failure := codex.ParseFailure(status, raw)
		message := imageDetail(raw, failure.Message)

		if secs, err := strconv.Atoi(retry); err == nil && secs > 0 && failure.ResetsAt.IsZero() {
			failure.ResetsAt = now.Add(time.Duration(secs) * time.Second)
		}

		code := cmpOr(failure.Code, "upstream_error")
		if codex.NotEntitled(status, raw, model) {
			// Another account may have the model (OmniRoute, issue #8307): a refusal that moves on.
			failure.Status, code = http.StatusForbidden, "model_not_entitled"
		}

		wait := s.imageRests.rest(key, now, failure.Status, failure.Type == "usage_limit_reached", failure.ResetsAt)

		return imageRefusal(failure.Status, code, cmpOr(message, http.StatusText(status)), wait), nil
	})
	if err == nil && resp.StatusCode == http.StatusOK {
		return answer, account, nil
	}

	return codex.ImageAnswer{}, "", s.failureOf(resp, err, "no Codex account is logged in")
}

// detailPattern finds the backend's {"detail": …}, which it answers some
// refusals with (OmniRoute's isCodexChatGptModelAccessError reads it).
var detailPattern = regexp.MustCompile(`"detail"\s*:\s*"([^"]*)"`)

// imageDetail is a refusal's detail, else the message read from it.
func imageDetail(raw []byte, message string) string {
	if detail := detailPattern.FindSubmatch(raw); detail != nil && len(detail[1]) > 0 {
		return string(detail[1])
	}

	return message
}

// codexImagePost sends one image call under an account with the headers
// chat sends (codexPost) and Accept JSON, as OmniRoute's codexImages does,
// with a turn id of its own (x-codex-image-turn-id, OmniRoute and Hermes). A
// token the backend refuses is refreshed once and the call sent again.
func (s *Server) codexImagePost(
	ctx context.Context, token *AccountToken, path string, body []byte,
) (*http.Response, error) {
	send := func(token AccountToken) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.codexURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("building the image call to Codex: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", codexUserAgent)
		req.Header["originator"] = []string{codexOriginator}
		req.Header["version"] = []string{codexCLIVersion}
		req.Header["chatgpt-account-id"] = []string{token.ExternalID}
		req.Header["x-codex-image-turn-id"] = []string{s.svc.newID("img")}

		resp, err := s.imageClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("calling Codex: %w", err)
		}

		return resp, nil
	}

	resp, err := send(*token)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	drain(resp)

	if *token, err = s.accounts.TokenAfterRefusal(ctx, *token); err != nil {
		return nil, err
	}

	return send(*token)
}

// antigravityImages makes the images under the Antigravity accounts in turn,
// in one call: Google makes n candidates (OmniRoute's candidateCount).
func (s *Server) antigravityImages(ctx context.Context, model string, req ImageRequest) (ImageResult, *ImageFailure) {
	if s.accounts == nil {
		return ImageResult{}, &ImageFailure{Status: http.StatusServiceUnavailable, Message: "no Antigravity account"}
	}

	if len(req.Images) > antigravity.MaxImageReferences {
		return ImageResult{}, &ImageFailure{
			Status:  http.StatusBadRequest,
			Message: fmt.Sprintf("Antigravity takes at most %d reference images", antigravity.MaxImageReferences),
		}
	}

	opts := antigravity.ImageOptions{
		Model: model, Count: req.N, Size: req.Size, AspectRatio: req.AspectRatio, ImageSize: req.ImageSize,
	}
	for _, img := range req.Images {
		opts.Images = append(opts.Images, antigravity.ImageInput{MIME: img.MIME, Data: img.Data})
	}

	var res ImageResult

	resp, err := s.underAccounts(ctx, antigravityRuntime, "", nil, func(token AccountToken) (*http.Response, error) {
		got, refusal, err := s.antigravityImage(ctx, token, model, req.Prompt, opts)
		if err != nil || refusal != nil {
			return refusal, err
		}

		res = ImageResult{Created: s.svc.clock.Now().Unix(), AccountID: token.AccountID}
		res.Tokens.Prompt, res.Tokens.Completion = got.InputTokens, got.OutputTokens

		for _, img := range got.Images {
			res.Images = append(res.Images, GeneratedImage{
				B64JSON: img.B64JSON, MIME: cmpOr(img.MIME, "image/png"), RevisedPrompt: img.RevisedPrompt,
			})
		}

		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	if err == nil && resp.StatusCode == http.StatusOK {
		return res, nil
	}

	return ImageResult{}, s.failureOf(resp, err, "no Antigravity account is logged in")
}

// antigravityImage makes the images under one account: within its requests
// in flight (antigravityState, as chat), with the IDE's user agent, its
// token refreshed once when refused.
func (s *Server) antigravityImage(
	ctx context.Context, token AccountToken, model, prompt string, opts antigravity.ImageOptions,
) (antigravity.ImageAnswer, *http.Response, error) {
	now := s.svc.clock.Now()
	key := restKey(ImageProviderAntigravity, token.AccountID, model)

	if token.Project == "" {
		// OmniRoute refuses it the same way: Google refuses a request without one.
		return antigravity.ImageAnswer{}, imageRefusal(http.StatusBadRequest, "no_project",
			"the Antigravity account has no project; log it in again", 0), nil
	}

	if until, resting := s.imageRests.resting(key, now); resting {
		return antigravity.ImageAnswer{}, imageRefusal(http.StatusTooManyRequests, "image_rate_limited",
			"the account's image quota rests", until.Sub(now)), nil
	}

	if _, entered := s.antigravity.enter(token.AccountID, ModelImagePrefix+model, now); !entered {
		return antigravity.ImageAnswer{}, imageRefusal(http.StatusTooManyRequests, "rate_limit_exceeded",
			"the Antigravity account is at its limit of requests in flight", time.Second), nil
	}

	defer s.antigravity.leave(token.AccountID)

	send := func(token AccountToken) (*http.Response, error) {
		opts.Project, opts.RequestID = token.Project, antigravity.ImageRequestID(now, s.svc.newID("img"))

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			s.antigravityURL+"/"+cloudcode.APIVersion+":generateContent",
			bytes.NewReader(antigravity.ImageBody(prompt, opts)))
		if err != nil {
			return nil, fmt.Errorf("building the image call to Antigravity: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", s.antigravity.identity.UserAgent(ctx))

		resp, err := s.imageClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("calling Antigravity: %w", err)
		}

		return resp, nil
	}

	resp, err := send(token)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		drain(resp)

		if token, err = s.accounts.TokenAfterRefusal(ctx, token); err == nil {
			resp, err = send(token)
		}
	}

	if err != nil {
		return antigravity.ImageAnswer{}, nil, err
	}

	status := resp.StatusCode

	raw, err := readImageAnswer(resp)
	if err != nil {
		return antigravity.ImageAnswer{}, nil, err
	}

	if status == http.StatusOK {
		got, err := antigravity.ParseImages(raw, prompt)

		var refused *antigravity.RefusedError

		switch {
		case errors.As(err, &refused):
			return antigravity.ImageAnswer{}, imageRefusal(imageRefusedStatus, "image_refused", err.Error(), 0),
				nil
		case err != nil:
			return antigravity.ImageAnswer{}, imageRefusal(http.StatusBadGateway, "upstream_failed", err.Error(), 0),
				nil
		}

		s.imageRests.served(key)

		return got, nil, nil
	}

	f := antigravity.ParseFailure(status, raw)

	switch f.Reason {
	case antigravity.ReasonTermsViolation:
		s.antigravityPark(ctx, token, now.Add(antigravitySuspended), "suspended by Google for its terms of service", f)
	case antigravity.ReasonVerification:
		s.antigravityPark(ctx, token, now.Add(antigravityVerify), "Google asks for the account to be verified", f)
	}

	reset := antigravity.QuotaResetAt(raw)
	if reset.IsZero() && f.RetryAfter > 0 {
		reset = now.Add(f.RetryAfter)
	}

	quotaUsedUp := f.Status == http.StatusTooManyRequests && f.Reason == antigravity.ReasonQuotaExhausted

	if f.Status == http.StatusTooManyRequests && reset.IsZero() {
		// The waits of omp and the opencode plugin when Google names none, as chat waits (antigravity_adapter.go).
		wait := antigravityRateWait
		if quotaUsedUp {
			wait = antigravityQuotaWait
		}

		reset = now.Add(wait)
	}

	wait := time.Duration(0)
	if f.Reason != antigravity.ReasonNoCapacity {
		wait = s.imageRests.rest(key, now, f.Status, quotaUsedUp, reset)
	}

	return antigravity.ImageAnswer{}, imageRefusal(f.Status, f.Code, f.Message, wait), nil
}

// dataURL is an image as a data: address, for a client that asked for a URL
// (OmniRoute answers response_format url so).
func dataURL(img GeneratedImage) string {
	return "data:" + cmpOr(img.MIME, "image/png") + ";base64," + img.B64JSON
}

// decodeReference reads a reference image given inline: a data: address or
// bare base64. An address on the web is refused, never fetched (OmniRoute's
// geminiInline.ts, for the same reason: no request to wherever a client says).
func decodeReference(ref string) (ImageInput, error) {
	ref = strings.TrimSpace(ref)

	switch {
	case ref == "":
		return ImageInput{}, fmt.Errorf("%w: a reference image is empty", ErrInvalid)
	case strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"):
		return ImageInput{}, fmt.Errorf("%w: a reference image on the web is not fetched; send it inline as "+
			"data:image/png;base64,... or as a file of a multipart edit", ErrInvalid)
	}

	mime := ""

	if rest, ok := strings.CutPrefix(ref, "data:"); ok {
		head, data, found := strings.Cut(rest, ",")
		if !found || !strings.HasSuffix(head, ";base64") {
			return ImageInput{}, fmt.Errorf("%w: a reference image's data: address is not base64", ErrInvalid)
		}

		mime, ref = strings.TrimSuffix(head, ";base64"), data
	}

	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(ref), ""))
	if err != nil {
		return ImageInput{}, fmt.Errorf("%w: a reference image is not base64", ErrInvalid)
	}

	return ImageInput{MIME: mime, Data: data}, nil
}
