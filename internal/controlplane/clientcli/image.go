package clientcli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Images from the terminal (issue #431): a Bot makes or edits an image with
// these, as it searches the web with `hypomnema web` (web.go). The images are
// saved as files, in the current folder unless --out names another, and
// their paths printed, one a line, as omp's generate_image and opencode's
// experimental_image_generate save theirs and say where; a Bot then sends a
// file into its chat with a MEDIA:<path> line (larkbot/media.go). The
// providers, their switches, order and models are what Settings → Gateway →
// Images shows and changes.

// imageList is a flag given once a value: --image a.png --image b.png.
type imageList []string

func (l *imageList) String() string { return strings.Join(*l, ",") }

func (l *imageList) Set(v string) error {
	*l = append(*l, v)

	return nil
}

// imageFlags are the flags of image generate and image edit.
func imageFlags(edit bool) func(fs *flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		if edit {
			fs.Var(&imageList{}, "image", "an image to edit or to use as a reference; give it once an image (up to 14)")
		}

		fs.String("size", "", "1024x1024, 1536x1024, 1024x1536, auto, or an aspect ratio such as 16:9")
		fs.Int("n", 0, "how many images, 1 to 10 (default 1)")
		fs.String("model", "", "the model: gpt-image-* (Codex), gemini-* (Antigravity) or <provider>/<model> "+
			"(default: each provider's own, as Settings → Gateway → Images sets it)")
		fs.String("provider", "", "ask only this provider: codex or antigravity (default: the first that answers)")
		fs.String("quality", "", "low, medium, high or auto (Codex)")
		fs.String("image-size", "", "1K, 2K or 4K (Antigravity)")
		fs.String("out", "", "the folder to save the images in, or a file name (default: the current folder)")
		jsonFlag(fs)
	}
}

func imageGenerate(ctx context.Context, e *env, fs *flag.FlagSet) error {
	return e.makeImages(ctx, fs, nil)
}

func imageEdit(ctx context.Context, e *env, fs *flag.FlagSet) error {
	paths, _ := fs.Lookup("image").Value.(*imageList)
	if paths == nil || len(*paths) == 0 {
		return fmt.Errorf("%w: image edit needs --image <file>, once an image", ErrUsage)
	}

	return e.makeImages(ctx, fs, *paths)
}

// makeImages asks for the images, saves them and prints where.
func (e *env) makeImages(ctx context.Context, fs *flag.FlagSet, inputs []string) error {
	body := openapi.ImageRequest{
		Prompt: strings.Join(fs.Args(), " "), Size: optional(fs, "size"), Model: optional(fs, "model"),
		Quality: optional(fs, "quality"), ImageSize: optional(fs, "image-size"),
	}

	if n := number(fs, "n"); n > 0 {
		body.N = &n
	}

	if p := str(fs, "provider"); p != "" {
		provider := openapi.ImageProvider(p)
		body.Provider = &provider
	}

	if len(inputs) > 0 {
		images := make([]openapi.ImageInput, 0, len(inputs))

		for _, path := range inputs {
			data, err := os.ReadFile(path) //nolint:gosec // the caller names the image it wants sent
			if err != nil {
				return fmt.Errorf("reading %s: %w", path, err)
			}

			mime := http.DetectContentType(data)
			images = append(images, openapi.ImageInput{Data: data, MimeType: &mime})
		}

		body.Images = &images
	}

	resp, err := e.api.GenerateImagesWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("making the image: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("making the image", resp.StatusCode(), resp.JSONDefault)
	}

	a := resp.JSON200

	paths, err := saveImages(str(fs, "out"), a.Images, time.Now())
	if err != nil {
		return err
	}

	saved := struct {
		Provider openapi.ImageProvider    `json:"provider"`
		Model    string                   `json:"model"`
		Created  int64                    `json:"created"`
		Paths    []string                 `json:"paths"`
		Errors   []openapi.WebSearchError `json:"errors,omitempty"`
	}{a.Provider, a.Model, a.Created, paths, val(a.Errors)}

	return e.print(fs, saved, func() {
		for _, p := range paths {
			e.printf("%s\n", p)
		}

		line := fmt.Sprintf("Made by %s with %s", a.Provider, a.Model)
		for i, f := range val(a.Errors) {
			sep := ", after "
			if i > 0 {
				sep = "; "
			}

			line += sep + f.Provider + " failed (" + f.Message + ")"
		}

		e.printf("%s.\n", line)
	})
}

// extensions are the files' extensions by the images' types.
var extensions = map[string]string{
	"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif",
}

// saveImages writes the images and returns their absolute paths. out is a
// folder (it exists, or ends with a separator), a file name whose extension
// becomes the image's own (opencode's image tool does the same), numbered
// when there are several, or empty for the current folder.
func saveImages(out string, images []openapi.GeneratedImage, now time.Time) ([]string, error) {
	dir, base := out, ""

	info, err := os.Stat(out)
	folder := out == "" || strings.HasSuffix(out, "/") || strings.HasSuffix(out, string(os.PathSeparator)) ||
		(err == nil && info.IsDir())

	if folder {
		if dir == "" {
			dir = "."
		}
	} else {
		dir, base = filepath.Dir(out), strings.TrimSuffix(filepath.Base(out), filepath.Ext(out))
	}

	if base == "" {
		base = "image-" + now.Format("20060102-150405")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("making %s: %w", dir, err)
	}

	paths := make([]string, 0, len(images))

	for i, img := range images {
		data, err := base64.StdEncoding.DecodeString(img.B64Json)
		if err != nil {
			return paths, fmt.Errorf("image %d is not base64: %w", i+1, err)
		}

		ext := extensions[img.MimeType]
		if ext == "" {
			ext = extensions[http.DetectContentType(data)]
		}

		if ext == "" {
			ext = ".png"
		}

		name := base
		if len(images) > 1 {
			name += fmt.Sprintf("-%d", i+1)
		}

		path, err := filepath.Abs(filepath.Join(dir, name+ext))
		if err != nil {
			return paths, err
		}

		if err := os.WriteFile(path, data, 0o600); err != nil {
			return paths, fmt.Errorf("saving %s: %w", path, err)
		}

		paths = append(paths, path)
	}

	if len(paths) == 0 {
		return nil, errors.New("the answer held no image")
	}

	return paths, nil
}

func imageProviders(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetImagesWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the image providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the image providers", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printImages(resp.JSON200) })
}

// printImages prints a line a provider, in order: its turn, name, state,
// model, accounts and usage.
func (e *env) printImages(images *openapi.Images) {
	for i, p := range images.Providers {
		state := strings.ReplaceAll(string(p.State), "_", " ")
		if p.State == openapi.ImageProviderLinkStateResting && p.RestingUntil != nil {
			state += " until " + p.RestingUntil.Local().Format("15:04")
		}

		model := p.Model + " (default)"
		if p.ModelChosen {
			model = p.Model + " (default " + p.DefaultModel + ")"
		}

		accounts := fmt.Sprintf("%d accounts", p.Accounts)
		if p.Accounts == 1 {
			accounts = "1 account"
		}

		e.printf("%d\t%s\t%s\t%s\t%s\ttoday %d made, %d failed; this month %d made, %d failed\n", i+1, p.Provider, state,
			model, accounts, p.Usage.Today.Answered, p.Usage.Today.Failed, p.Usage.Month.Answered, p.Usage.Month.Failed)
	}
}

func imageProvidersSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider, state := fs.Arg(0), fs.Arg(1)
	if state != "on" && state != "off" {
		return fmt.Errorf("%w: image providers set <provider> on|off", ErrUsage)
	}

	on := state == "on"

	return e.changeImageProvider(ctx, fs, provider, openapi.ImageProviderChange{On: &on},
		"image provider "+provider+" is "+state)
}

func imageProvidersModel(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider, model := fs.Arg(0), fs.Arg(1)
	if model == "default" {
		model = ""
	}

	done := "image provider " + provider + " makes images with " + model
	if model == "" {
		done = "image provider " + provider + " makes images with its default model"
	}

	return e.changeImageProvider(ctx, fs, provider, openapi.ImageProviderChange{Model: &model}, done)
}

func (e *env) changeImageProvider(
	ctx context.Context, fs *flag.FlagSet, provider string, change openapi.ImageProviderChange, done string,
) error {
	resp, err := e.api.SetImageProviderWithResponse(ctx, openapi.ImageProvider(provider), change)
	if err != nil {
		return fmt.Errorf("changing %s: %w", provider, err)
	}

	if resp.JSON200 == nil {
		return apiError("changing "+provider, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s\n", done) })
}

func imageOrder(ctx context.Context, e *env, fs *flag.FlagSet) error {
	var body openapi.ImageOrder
	for _, p := range fs.Args() {
		body.Providers = append(body.Providers, openapi.ImageProvider(p))
	}

	resp, err := e.api.SetImageOrderWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("ordering the image providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("ordering the image providers", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printImages(resp.JSON200) })
}
