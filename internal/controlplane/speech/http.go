package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// userAgent names this program to the providers, as web search does.
const userAgent = "hypomnema"

// send asks a provider and returns the body of a success, or its refusal, as
// web search's send does (websearch/providers.go); a refusal that may pass is
// asked again (retry.go) when the body can be sent again.
func (s *Service) send(req *http.Request, limit int64) ([]byte, error) {
	var body []byte

	err := s.retrying(req.Context(), func(attempt int) error {
		if attempt > 0 {
			if req.GetBody == nil && req.Body != nil {
				return errNoReplay
			}

			if req.GetBody != nil {
				replay, err := req.GetBody()
				if err != nil {
					return err
				}

				req.Body = replay
			}
		}

		var err error

		body, err = s.sendOnce(req, limit)

		return err
	})

	return body, err
}

// sendOnce asks a provider once.
func (s *Service) sendOnce(req *http.Request, limit int64) ([]byte, error) {
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return nil, fmt.Errorf("no answer: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, refusal(resp, body, s.now())
	}

	if int64(len(body)) > limit {
		return nil, fmt.Errorf("the answer is larger than %d bytes", limit)
	}

	return body, nil
}

// call sends a request with a JSON body, or none, and reads a JSON success into into, when into is not nil.
func (s *Service) call(ctx context.Context, method, endpoint string, headers map[string]string, body, into any,
	limit int64,
) error {
	var reader io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}

		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	req.Header.Set("Accept", "application/json")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	raw, err := s.send(req, limit)
	if err != nil || into == nil {
		return err
	}

	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("reading its answer: %w", err)
	}

	return nil
}

// field is one text field of a form.
type field struct{ name, value string }

// postForm posts a multipart form of the recording, under the field `file`,
// and fields; the recording is streamed from its source, as MemoryOS streams
// a recording into Soniox's upload (SonioxAsync.segments), so 128 MB never
// sits twice in memory.
func (s *Service) postForm(ctx context.Context, endpoint string, headers map[string]string, audio Audio,
	fields []field, limit int64,
) ([]byte, error) {
	src, err := audio.Open()
	if err != nil {
		return nil, fmt.Errorf("reading the recording: %w", err)
	}
	defer src.Close() //nolint:errcheck // Read only.

	pr, pw := io.Pipe()
	form := multipart.NewWriter(pw)

	go func() {
		pw.CloseWithError(writeForm(form, audio, src, fields))
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, pr)
	if err != nil {
		_ = pr.Close()

		return nil, err
	}

	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	body, err := s.send(req, limit)
	_ = pr.CloseWithError(io.ErrClosedPipe) // Stops the writer when the provider answered before reading it all.

	return body, err
}

func writeForm(form *multipart.Writer, audio Audio, src io.Reader, fields []field) error {
	for _, f := range fields {
		if err := form.WriteField(f.name, f.value); err != nil {
			return err
		}
	}

	name := audio.Name
	if name == "" {
		name = "audio"
	}

	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+escapeQuotes(name)+`"`)

	kind := audio.MIME
	if kind == "" {
		kind = "application/octet-stream"
	}

	h.Set("Content-Type", kind)

	part, err := form.CreatePart(h)
	if err != nil {
		return err
	}

	if _, err := io.Copy(part, src); err != nil {
		return err
	}

	return form.Close()
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func escapeQuotes(s string) string { return quoteEscaper.Replace(s) }
