package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// POST /v1/images/generations and /v1/images/edits (issue #431), OpenAI's
// Images API as OmniRoute (src/app/api/v1/images/{generations,edits}),
// CLIProxyAPI (internal/api/server_routes.go) and 9router-go serve it: a JSON
// body, or for an edit a multipart form with the files, answered
// {created, data: [{b64_json, revised_prompt?}]}. Any key the gateway knows
// may ask, whatever models it may use, as for a web search. A reference image
// comes inline in either route: every spelling the references' clients send
// (image, images[], image_url, input_references[] of omp and opencode; codex-rs
// images[].image_url), never an address to fetch. Each request is one usage
// row on its key, as model image/<model>.

// imageBody is an image request in JSON.
type imageBody struct {
	Prompt         string            `json:"prompt"`
	Model          string            `json:"model"`
	Provider       string            `json:"provider"`
	N              int               `json:"n"`
	Size           string            `json:"size"`
	Quality        string            `json:"quality"`
	Background     string            `json:"background"`
	ResponseFormat string            `json:"response_format"`
	AspectRatio    string            `json:"aspect_ratio"`
	ImageSize      string            `json:"image_size"`
	Image          json.RawMessage   `json:"image"`
	Images         []json.RawMessage `json:"images"`
	ImageURL       json.RawMessage   `json:"image_url"`
	References     []json.RawMessage `json:"input_references"`
	Mask           json.RawMessage   `json:"mask"`
}

// referenceOf reads one reference as any client spells it: a string, or an
// object with image_url (a string or {url}) or url. A file id is the OpenAI
// files API's, which the gateway does not have.
func referenceOf(raw json.RawMessage) (ImageInput, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return decodeReference(text)
	}

	var ref struct {
		ImageURL json.RawMessage `json:"image_url"`
		URL      string          `json:"url"`
		FileID   string          `json:"file_id"`
	}

	if err := json.Unmarshal(raw, &ref); err != nil {
		return ImageInput{}, fmt.Errorf("%w: a reference image is a data: address, base64, or {image_url}", ErrInvalid)
	}

	if ref.FileID != "" {
		return ImageInput{}, fmt.Errorf("%w: a file id is not taken; send the image inline", ErrInvalid)
	}

	if len(ref.ImageURL) > 0 {
		var nested struct {
			URL string `json:"url"`
		}

		if json.Unmarshal(ref.ImageURL, &text) == nil {
			return decodeReference(text)
		}

		if json.Unmarshal(ref.ImageURL, &nested) == nil {
			return decodeReference(nested.URL)
		}
	}

	return decodeReference(ref.URL)
}

// request is the body as the gateway's request, its references read.
func (b imageBody) request() (ImageRequest, error) {
	if len(b.Mask) > 0 && string(b.Mask) != "null" {
		return ImageRequest{}, fmt.Errorf("%w: a mask is not taken by the subscriptions' image models", ErrInvalid)
	}

	req := ImageRequest{
		Prompt: b.Prompt, Model: b.Model, Provider: b.Provider, N: b.N, Size: b.Size, Quality: b.Quality,
		Background: b.Background, AspectRatio: b.AspectRatio, ImageSize: b.ImageSize,
	}

	var refs []json.RawMessage

	for _, one := range []json.RawMessage{b.Image, b.ImageURL} {
		if len(one) > 0 && string(one) != "null" {
			refs = append(refs, one)
		}
	}

	refs = append(append(refs, b.Images...), b.References...)

	for _, raw := range refs {
		img, err := referenceOf(raw)
		if err != nil {
			return ImageRequest{}, err
		}

		req.Images = append(req.Images, img)
	}

	return req, nil
}

// multipartMemory is how much of a form is kept in memory; the rest goes to
// temporary files, removed after.
const multipartMemory = 32 << 20

// formRequest reads an edit's form: OpenAI's fields, and the images as files
// named image, image[] (omp, opencode) or images[].
func formRequest(r *http.Request) (ImageRequest, string, error) {
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		return ImageRequest{}, "", fmt.Errorf("%w: the form cannot be read: %w", ErrInvalid, err)
	}

	defer r.MultipartForm.RemoveAll() //nolint:errcheck // Temporary files; nothing to do when one stays.

	form := r.MultipartForm
	value := func(name string) string {
		if v := form.Value[name]; len(v) > 0 {
			return v[0]
		}

		return ""
	}

	if len(form.File["mask"]) > 0 {
		return ImageRequest{}, "", fmt.Errorf("%w: a mask is not taken by the subscriptions' image models", ErrInvalid)
	}

	req := ImageRequest{
		Prompt: value("prompt"), Model: value("model"), Provider: value("provider"), Size: value("size"),
		Quality: value("quality"), Background: value("background"), AspectRatio: value("aspect_ratio"),
		ImageSize: value("image_size"),
	}

	if n := value("n"); n != "" {
		count, err := strconv.Atoi(n)
		if err != nil {
			return ImageRequest{}, "", fmt.Errorf("%w: n is a number", ErrInvalid)
		}

		req.N = count
	}

	for _, field := range []string{"image", "image[]", "images", "images[]"} {
		for _, fh := range form.File[field] {
			img, err := formFile(fh)
			if err != nil {
				return ImageRequest{}, "", err
			}

			req.Images = append(req.Images, img)
		}
	}

	return req, value("response_format"), nil
}

func formFile(fh *multipart.FileHeader) (ImageInput, error) {
	if fh.Size > maxImageInput {
		return ImageInput{}, fmt.Errorf("%w: %s is larger than %d MiB", ErrInvalid, fh.Filename, maxImageInput>>20)
	}

	f, err := fh.Open()
	if err != nil {
		return ImageInput{}, fmt.Errorf("reading %s: %w", fh.Filename, err)
	}
	defer f.Close() //nolint:errcheck // Read only.

	data, err := io.ReadAll(io.LimitReader(f, maxImageInput+1))
	if err != nil {
		return ImageInput{}, fmt.Errorf("reading %s: %w", fh.Filename, err)
	}

	return ImageInput{MIME: fh.Header.Get("Content-Type"), Data: data}, nil
}

func (s *Server) imageGenerations(w http.ResponseWriter, r *http.Request) { s.images(w, r, false) }

func (s *Server) imageEdits(w http.ResponseWriter, r *http.Request) { s.images(w, r, true) }

type imageData struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type imageUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type imageAnswer struct {
	Created int64       `json:"created"`
	Data    []imageData `json:"data"`
	Usage   *imageUsage `json:"usage,omitempty"`
}

// images answers either route: an edit may come as a form.
func (s *Server) images(w http.ResponseWriter, r *http.Request, edit bool) {
	k, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxImageRequestBytes)

	var (
		req    ImageRequest
		format string
		err    error
	)

	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))

	if edit && strings.HasPrefix(media, "multipart/") {
		req, format, err = formRequest(r)
	} else {
		var body imageBody
		if err = json.NewDecoder(r.Body).Decode(&body); err != nil {
			err = fmt.Errorf("%w: the body is OpenAI's image request: {\"prompt\": \"...\", \"model\": ..., \"n\": ...}",
				ErrInvalid)
		} else {
			req, err = body.request()
			format = body.ResponseFormat
		}
	}

	if err == nil && edit && len(req.Images) == 0 {
		err = fmt.Errorf("%w: an edit needs at least one image", ErrInvalid)
	}

	if err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", err.Error())

		return
	}

	start := s.svc.clock.Now()
	res, err := s.MakeImages(r.Context(), req)
	s.recordImages(r.Context(), ImageCall{
		KeyID: k.ID, Request: req, Result: res, Err: err, Duration: s.svc.clock.Now().Sub(start),
	})

	if err != nil {
		s.imagesFailed(w, r, err)

		return
	}

	out := imageAnswer{Created: res.Created, Data: make([]imageData, 0, len(res.Images))}
	if out.Created == 0 {
		out.Created = s.svc.clock.Now().Unix()
	}

	for _, img := range res.Images {
		d := imageData{B64JSON: img.B64JSON, RevisedPrompt: img.RevisedPrompt}
		if format == "url" {
			d = imageData{URL: dataURL(img), RevisedPrompt: img.RevisedPrompt}
		}

		out.Data = append(out.Data, d)
	}

	if t := res.Tokens; t.Prompt+t.Completion > 0 {
		out.Usage = &imageUsage{InputTokens: t.Prompt, OutputTokens: t.Completion, TotalTokens: t.Prompt + t.Completion}
	}

	writeJSON(w, http.StatusOK, out)
}

// imagesFailed answers a request no provider made images for.
func (s *Server) imagesFailed(w http.ResponseWriter, r *http.Request, err error) {
	var failed *ImageError

	switch {
	case r.Context().Err() != nil:
		// The client left.
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", err.Error())
	case errors.As(err, &failed):
		status := failed.Status()

		switch status {
		case http.StatusTooManyRequests:
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(failed.RetryAfter().Round(time.Second).Seconds()))))
			writeError(w, status, typeRateLimit, "image_rate_limited", err.Error())
		case http.StatusServiceUnavailable:
			writeError(w, status, typeServer, "no_image_provider", err.Error())
		case http.StatusBadGateway:
			writeError(w, status, typeServer, "upstream_failed", err.Error())
		default:
			writeError(w, status, typeInvalidRequest, "image_refused", err.Error())
		}
	default:
		s.internal(w, r, "modelgateway.images_failed", err)
	}
}

// ImageCall is one image request, to be recorded on a key.
type ImageCall struct {
	KeyID    string
	Request  ImageRequest
	Result   ImageResult
	Err      error
	Duration time.Duration
}

// RecordImageCall records an image request on its key as model
// image/<model>: the provider and model that made it, or for one that
// failed the last tried and every provider's reason; the tokens the provider
// reported, none invented.
func (s *Service) RecordImageCall(ctx context.Context, c ImageCall) error {
	res := c.Result
	u := Usage{
		KeyID: c.KeyID, Provider: res.Provider, UpstreamModel: res.Model, Attempts: len(res.Errors) + 1,
		Outcome: OutcomeOK, Status: http.StatusOK, AccountID: res.AccountID, Tokens: res.Tokens, Duration: c.Duration,
	}

	var failed *ImageError

	switch {
	case c.Err == nil:
	case errors.As(c.Err, &failed) && len(failed.Failures) > 0:
		last := failed.Failures[len(failed.Failures)-1]
		u.Provider, u.UpstreamModel, u.Attempts = last.Provider, last.Model, len(failed.Failures)
		u.Outcome, u.Status, u.Error = OutcomeUpstreamError, failed.Status(), c.Err.Error()
	case errors.Is(c.Err, context.Canceled):
		u.Outcome, u.Status, u.Error = OutcomeClientGone, 0, c.Err.Error()
	case errors.Is(c.Err, ErrInvalid):
		// The request was refused before any provider: nothing was served.
		return nil
	default:
		u.Outcome, u.Status, u.Error = OutcomeUpstreamError, 0, c.Err.Error()
	}

	if u.UpstreamModel == "" {
		u.UpstreamModel = "auto"
	}

	u.Model = ModelImagePrefix + u.UpstreamModel

	return s.recordUsage(ctx, u)
}

// recordImages records an image request, whether or not the client stayed.
func (s *Server) recordImages(ctx context.Context, c ImageCall) {
	if err := s.svc.RecordImageCall(context.WithoutCancel(ctx), c); err != nil {
		s.logger.ErrorContext(ctx, "recording an image call",
			slog.String("event", "modelgateway.image_usage_failed"), slog.String("error", err.Error()))
	}
}
