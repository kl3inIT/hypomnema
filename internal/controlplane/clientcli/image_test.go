package clientcli

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// theImages stands for the control plane's images: it keeps what each chat
// asked and answers two PNGs from Codex after Antigravity failed.
type theImages struct {
	chat  string
	asked modelgateway.ImageRequest
}

var pngImage = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR a picture")

func (m *theImages) Make(
	_ context.Context, chatID string, req modelgateway.ImageRequest,
) (modelgateway.ImageResult, error) {
	m.chat, m.asked = chatID, req

	img := modelgateway.GeneratedImage{B64JSON: base64.StdEncoding.EncodeToString(pngImage), MIME: "image/png"}
	out := modelgateway.ImageResult{
		Provider: "codex", Model: "gpt-image-2", Created: 1,
		Errors: []modelgateway.ImageFailure{{Provider: "antigravity", Model: "gemini-3.1-flash-image", Message: "limited"}},
	}

	for range max(req.N, 1) {
		out.Images = append(out.Images, img)
	}

	return out, nil
}

// theImageSettings keeps the providers' settings in memory.
type theImageSettings struct {
	order []string
	off   map[string]bool
	model map[string]string
}

func (s *theImageSettings) ImageSettings(context.Context) ([]modelgateway.ImageProviderSetting, error) {
	out := make([]modelgateway.ImageProviderSetting, 0, len(s.order))
	for _, p := range s.order {
		model, chosen := modelgateway.DefaultImageModel(p), false
		if m := s.model[p]; m != "" {
			model, chosen = m, true
		}

		out = append(out, modelgateway.ImageProviderSetting{Provider: p, On: !s.off[p], Model: model, Chosen: chosen})
	}

	return out, nil
}

func (s *theImageSettings) ImageCounts(context.Context, time.Time) (map[string]modelgateway.ImageCount, error) {
	return map[string]modelgateway.ImageCount{"codex": {
		Today: modelgateway.ImageTally{Answered: 2}, Month: modelgateway.ImageTally{Answered: 5, Failed: 1},
	}}, nil
}

func (s *theImageSettings) ImageRests(context.Context) (map[string]map[string]time.Time, error) {
	return map[string]map[string]time.Time{}, nil
}

func (s *theImageSettings) SetImageProvider(_ context.Context, provider string, on bool) error {
	s.off[provider] = !on

	return nil
}

func (s *theImageSettings) SetImageModel(_ context.Context, provider, model string) error {
	s.model[provider] = model

	return nil
}

func (s *theImageSettings) SetImageOrder(_ context.Context, providers []string) error {
	s.order = providers

	return nil
}

// Scenario: a Bot's pi makes two images and edits one with the CLI. The
// request is its chat's, so the control plane records it on that chat's key;
// the images are saved where --out says, named after the file asked for and
// numbered, and their absolute paths printed one a line, then who made them
// after whom; an edit sends the image files' bytes.
func TestABotMakesAndEditsImages(t *testing.T) {
	t.Parallel()

	images := &theImages{}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithImages(images, &theImageSettings{}) })
	ctx := t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "pictures")
	require.NoError(t, err)

	piToken, err := w.svc.AddBotToken(ctx, "pi", chat.ID, "coordinator")
	require.NoError(t, err)
	w.loginWith(t, piToken)

	dir := t.TempDir()

	out, err := w.cmd(t, "image", "generate", "a", "red", "fox", "--n", "2", "--size", "1536x1024",
		"--model", "gpt-image-2.5-flare", "--out", filepath.Join(dir, "fox.jpg"))
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3, out)
	assert.Equal(t, filepath.Join(dir, "fox-1.png"), lines[0], "the extension is the image's own")
	assert.Equal(t, filepath.Join(dir, "fox-2.png"), lines[1])
	assert.Equal(t, "Made by codex with gpt-image-2, after antigravity failed (gemini-3.1-flash-image: limited).",
		lines[2])

	saved, err := os.ReadFile(lines[0])
	require.NoError(t, err)
	assert.Equal(t, pngImage, saved)

	assert.Equal(t, chat.ID, images.chat)
	assert.Equal(t, "a red fox", images.asked.Prompt)
	assert.Equal(t, 2, images.asked.N)
	assert.Equal(t, "gpt-image-2.5-flare", images.asked.Model)

	ref := filepath.Join(dir, "ref.png")
	require.NoError(t, os.WriteFile(ref, pngImage, 0o600))

	out, err = w.cmd(t, "image", "edit", "add", "a", "hat", "--image", ref, "--image", ref, "--out", dir+"/")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, filepath.Join(dir, "image-")), out)
	require.Len(t, images.asked.Images, 2)
	assert.Equal(t, pngImage, images.asked.Images[0].Data)
	assert.Equal(t, "image/png", images.asked.Images[0].MIME)

	_, err = w.cmd(t, "image", "edit", "no", "image")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--image")
}

// Scenario: the owner lists the image providers, sets Antigravity's model to
// try a newer one, puts it first and switches Codex off, from the terminal.
func TestTheOwnerSetsTheImageProvidersFromTheTerminal(t *testing.T) {
	t.Parallel()

	settings := &theImageSettings{
		order: []string{"codex", "antigravity"}, off: map[string]bool{}, model: map[string]string{},
	}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithImages(&theImages{}, settings) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "image", "providers")
	require.NoError(t, err)
	assert.Equal(t, "1\tcodex\tnot set up\tgpt-image-2 (default)\t0 accounts\t"+
		"today 2 made, 0 failed; this month 5 made, 1 failed\n"+
		"2\tantigravity\tnot set up\tgemini-3.1-flash-image (default)\t0 accounts\t"+
		"today 0 made, 0 failed; this month 0 made, 0 failed\n", out)

	out, err = w.cmd(t, "image", "providers", "model", "antigravity", "gemini-nano-banana-2.1")
	require.NoError(t, err)
	assert.Equal(t, "image provider antigravity makes images with gemini-nano-banana-2.1\n", out)
	assert.Equal(t, "gemini-nano-banana-2.1", settings.model["antigravity"])

	_, err = w.cmd(t, "image", "order", "antigravity", "codex")
	require.NoError(t, err)
	assert.Equal(t, []string{"antigravity", "codex"}, settings.order)

	_, err = w.cmd(t, "image", "providers", "set", "codex", "off")
	require.NoError(t, err)
	assert.True(t, settings.off["codex"])

	out, err = w.cmd(t, "image", "providers", "model", "antigravity", "default")
	require.NoError(t, err)
	assert.Contains(t, out, "its default model")
	assert.Empty(t, settings.model["antigravity"])
}
