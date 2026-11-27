package videos

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
)

// The templates of a video (issue #1230, rows 3-5): a template is a stored document, one JSON file of templates/,
// with a name, a use case, a target length, the aspects it is made in (the first its default), whether it has
// captions, the sections its lines are written against, each with a purpose and a word budget at 2.5 words a
// second, and the look it starts from: the sections of a look it sets, its cards always among them, the others the
// saved default's. A make with a template takes its aspect and captions when the request leaves them out, and its
// look's sections where the request sends none; a card the template has is titled with the video's title. The shape
// is aidemo's starter (src/starter.ts:10-141: a length, scenes at ~2.5 words a second, an intro and an outro) kept as
// data, not code; the word budgets are aidemo's script template's (docs/blog/demo-video-script-template), and each
// file says what it is learned from. The first five are built in; more are added as files.

// WordsPerSecond is the narration rate a section's words are budgeted at: aidemo's planning figure for English
// (src/lint.ts:63-64, starter.ts:34), which the issue takes for Vietnamese too.
const WordsPerSecond = 2.5

//go:embed templates/*.json
var templateFiles embed.FS

// Template is a stored template.
type Template struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	UseCase  string         `json:"use_case"`
	Length   TemplateLength `json:"length"`
	Aspects  []string       `json:"aspects"`
	Captions bool           `json:"captions"`
	Sections []Section      `json:"sections"`
	// Look is the sections of the look it sets; its cards are always set, a card's title left empty for the video's.
	Look LookInput `json:"look"`
	// From is what it is learned from.
	From string `json:"from"`
}

// TemplateLength is how long a video of the template is meant to be, in seconds.
type TemplateLength struct {
	MinS int `json:"min_s"`
	MaxS int `json:"max_s"`
}

// Section is a part of a template's script: what its lines do and how many words they have.
type Section struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	Words   int    `json:"words"`
}

// Seconds is how long a section's words take at WordsPerSecond.
func (s Section) Seconds() float64 { return float64(s.Words) / WordsPerSecond }

// Words is how many words the template's sections have.
func (t Template) Words() int {
	n := 0
	for _, s := range t.Sections {
		n += s.Words
	}

	return n
}

// Cards are the template's cards with their defaults, each title empty for the video's; nil for none.
func (t Template) Cards() *compose.Cards {
	c, err := cardsOf(t.titled("x").Cards)
	if err != nil || c == nil {
		return nil
	}

	for _, card := range []*compose.Card{c.Title, c.End} {
		if card != nil {
			card.Title = ""
		}
	}

	return c
}

// TemplateLook is a template with the look it starts from now: its sections over the saved default, its cards apart
// (Template.Cards), since they take a video's title.
type TemplateLook struct {
	Template
	Look Look
}

// Templates are the templates with the look each starts from over the saved default.
func (s *Service) Templates(ctx context.Context) ([]TemplateLook, error) {
	saved, err := s.savedLook(ctx, store.New(s.db))
	if err != nil {
		return nil, err
	}

	out := make([]TemplateLook, 0, len(builtIn))

	for _, t := range builtIn {
		in := t.Look
		in.Cards = nil

		look, err := resolveLook(&in, saved)
		if err != nil {
			return nil, err
		}

		look.Cards, look.FellBackFrom = nil, ""
		out = append(out, TemplateLook{Template: t, Look: look})
	}

	return out, nil
}

var templateIDs = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// builtIn are the templates of templates/, in their files' order.
var builtIn = mustTemplates(templateFiles)

func mustTemplates(files fs.FS) []Template {
	out, err := loadTemplates(files)
	if err != nil {
		panic(err)
	}

	return out
}

// loadTemplates reads and checks each template of templates/; a field it does not know is refused.
func loadTemplates(files fs.FS) ([]Template, error) {
	names, err := fs.Glob(files, "templates/*.json")
	if err != nil {
		return nil, err
	}

	var out []Template

	for _, name := range names {
		raw, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, err
		}

		var t Template

		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()

		if err := dec.Decode(&t); err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}

		if err := t.check(); err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}

		if slices.ContainsFunc(out, func(o Template) bool { return o.ID == t.ID }) {
			return nil, fmt.Errorf("template %s: another template is %s", name, t.ID)
		}

		out = append(out, t)
	}

	return out, nil
}

func (t Template) check() error {
	switch {
	case !templateIDs.MatchString(t.ID) || len(t.ID) > 40:
		return fmt.Errorf("the id %q is not lowercase words joined by -", t.ID)
	case strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.UseCase) == "":
		return fmt.Errorf("a template needs a name and a use case")
	case t.Length.MinS <= 0 || t.Length.MinS > t.Length.MaxS:
		return fmt.Errorf("its length %d-%d s is not a range of seconds", t.Length.MinS, t.Length.MaxS)
	case len(t.Aspects) == 0:
		return fmt.Errorf("a template needs an aspect")
	case len(t.Sections) == 0:
		return fmt.Errorf("a template needs a section")
	case t.Look.Cards == nil:
		return fmt.Errorf("a template says its cards, {} for none")
	}

	for _, a := range t.Aspects {
		if !knownAspect(a) {
			return fmt.Errorf("aspect %q is not 9:16, 16:9 or 886x1920", a)
		}
	}

	for _, s := range t.Sections {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Purpose) == "" || s.Words <= 0 {
			return fmt.Errorf("section %q needs a name, a purpose and words", s.Name)
		}
	}

	for _, c := range []*CardInput{t.Look.Cards.Title, t.Look.Cards.End} {
		if c != nil && c.Title != "" {
			return fmt.Errorf("a template's card takes the video's title, not %q", c.Title)
		}
	}

	if _, err := resolveLook(t.titled("x"), DefaultLook()); err != nil {
		return err
	}

	return nil
}

// Templates are the templates, in their files' order.
func Templates() []Template {
	return slices.Clone(builtIn)
}

// TemplateByID is the template of that id.
func TemplateByID(id string) (Template, bool) {
	i := slices.IndexFunc(builtIn, func(t Template) bool { return t.ID == id })
	if i < 0 {
		return Template{}, false
	}

	return builtIn[i], true
}

// templateOf is the template a request names, nil for none.
func templateOf(id string) (*Template, error) {
	if id == "" {
		return nil, nil //nolint:nilnil // No template is none, and no error.
	}

	t, ok := TemplateByID(id)
	if !ok {
		return nil, fmt.Errorf("%w: no template is named %q; the templates are %s", ErrInvalid, id,
			strings.Join(templateNames(), ", "))
	}

	return &t, nil
}

func templateNames() []string {
	out := make([]string, 0, len(builtIn))
	for _, t := range builtIn {
		out = append(out, t.ID)
	}

	return out
}

// aspectWith is the aspect of a make with the template: the one asked, which must be one of its, else its first.
func aspectWith(t *Template, asked string) (string, error) {
	if t == nil {
		return asked, nil
	}

	if asked == "" {
		return t.Aspects[0], nil
	}

	if !slices.Contains(t.Aspects, asked) {
		return "", fmt.Errorf("%w: the template %s is made in %s, not %s", ErrInvalid, t.ID,
			strings.Join(t.Aspects, " or "), asked)
	}

	return asked, nil
}

// captionsWith is whether a make with the template has captions: as asked, else the template's.
func captionsWith(t *Template, asked *bool) *bool {
	if asked != nil || t == nil {
		return asked
	}

	on := t.Captions

	return &on
}

// lookWith is the look a make with the template sends: the template's sections, each one the request sent in its
// place, the template's cards titled with the video's title. Without a template it is the request's.
func lookWith(t *Template, title string, sent *LookInput) *LookInput {
	if t == nil {
		return sent
	}

	out := t.titled(title)
	if sent == nil {
		return out
	}

	if sent.Captions != nil {
		out.Captions = sent.Captions
	}

	if sent.Frame != nil {
		out.Frame = sent.Frame
	}

	if sent.Background != nil {
		out.Background = sent.Background
	}

	if sent.Music != nil {
		out.Music = sent.Music
	}

	if sent.Cards != nil {
		out.Cards = sent.Cards
	}

	return out
}

// titled is the template's look with each of its cards titled title, cut to a card's longest title.
func (t Template) titled(title string) *LookInput {
	out := t.Look

	c := *t.Look.Cards
	for _, card := range []**CardInput{&c.Title, &c.End} {
		if *card != nil {
			v := **card
			v.Title = cardTitle(title)
			*card = &v
		}
	}

	out.Cards = &c

	return &out
}

// cardTitle is a video's title as a card holds it: at most MaxCardTitle characters, cut at a space when one is
// near (an inference: no reference titles a card from a longer title).
func cardTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if utf8.RuneCountInString(title) <= compose.MaxCardTitle {
		return title
	}

	cut := []rune(title)[:compose.MaxCardTitle]
	for i := len(cut) - 1; i > len(cut)/2; i-- {
		if cut[i] == ' ' {
			cut = cut[:i]

			break
		}
	}

	return strings.TrimSpace(string(cut))
}

// knownAspect says whether an aspect is one a video is made in.
func knownAspect(a string) bool {
	return slices.Contains(compose.Aspects, compose.Aspect(a))
}
