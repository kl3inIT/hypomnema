package videos

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
)

// The templates (issue #1230, rows 3-5): five stored documents, the issue's table, each made in its aspects with its
// captions and its look's sections, its cards titled with the video's title.

// The built-in templates are the issue's table, in its order, each with its lengths, aspects and sections; each
// one's words at 2.5 a second and its cards fit its length.
func TestTheBuiltInTemplates(t *testing.T) {
	t.Parallel()

	type row struct {
		id       string
		min, max int
		aspects  []string
		captions bool
		words    []int
		title    bool
		end      bool
	}

	want := []row{
		{"app-ad", 15, 30, []string{"9:16"}, true, []int{10, 15, 15, 15, 10}, false, true},
		{"feature-announcement", 30, 30, []string{"9:16", "16:9"}, true, []int{18, 30, 25}, false, false},
		{"product-walkthrough", 60, 90, []string{"16:9"}, true, []int{30, 20, 35, 35, 35, 25, 15}, true, true},
		{"onboarding", 20, 60, []string{"16:9"}, true, []int{10, 15, 15, 15, 10}, false, false},
		{"app-store-preview", 15, 30, []string{"886x1920"}, false, []int{15, 15, 15}, false, false},
	}

	got := Templates()
	require.Len(t, got, len(want))

	for i, w := range want {
		tpl := got[i]
		assert.Equal(t, w.id, tpl.ID)
		assert.Equal(t, TemplateLength{w.min, w.max}, tpl.Length, w.id)
		assert.Equal(t, w.aspects, tpl.Aspects, w.id)
		assert.Equal(t, w.captions, tpl.Captions, w.id)

		words := make([]int, 0, len(tpl.Sections))
		for _, s := range tpl.Sections {
			words = append(words, s.Words)
			assert.InDelta(t, float64(s.Words)/2.5, s.Seconds(), 1e-9)
		}

		assert.Equal(t, w.words, words, w.id)

		cards := tpl.Cards()
		assert.Equal(t, w.title, cards != nil && cards.Title != nil, "%s's title card", w.id)
		assert.Equal(t, w.end, cards != nil && cards.End != nil, "%s's end card", w.id)

		var cardsS float64
		if cards != nil {
			cardsS = float64(cards.TitleMs()+cards.EndMs()) / 1000
		}

		length := float64(tpl.Words())/WordsPerSecond + cardsS
		assert.LessOrEqual(t, length, float64(w.max), "%s fits its longest", w.id)
		assert.GreaterOrEqual(t, length, float64(w.min)-1, "%s fills its shortest", w.id)

		byID, ok := TemplateByID(w.id)
		require.True(t, ok)
		assert.Equal(t, tpl, byID)
	}

	store, _ := TemplateByID("app-store-preview")
	assert.Equal(t, compose.ChromeFull, compose.Chrome(*store.Look.Frame.Chrome), "the screen only")

	_, ok := TemplateByID("nope")
	assert.False(t, ok)
}

// A template file that is not a good template is refused: an unknown field, a card titled, an aspect not made, no
// section, an id taken twice.
func TestABadTemplateFileIsRefused(t *testing.T) {
	t.Parallel()

	good := `{"id":"a","name":"A","use_case":"U","length":{"min_s":10,"max_s":20},"aspects":["9:16"],` +
		`"captions":true,"sections":[{"name":"S","purpose":"P","words":10}],"look":{"cards":{}},"from":"f"}`

	list, err := loadTemplates(fstest.MapFS{"templates/a.json": {Data: []byte(good)}})
	require.NoError(t, err)
	require.Len(t, list, 1)

	for name, raw := range map[string]string{
		"an unknown field": `{"id":"a","name":"A","use_case":"U","length":{"min_s":10,"max_s":20},"aspects":["9:16"],` +
			`"sections":[{"name":"S","purpose":"P","words":10}],"look":{"cards":{}},"tone":"warm"}`,
		"a card without words": `{"id":"a","name":"A","use_case":"U","length":{"min_s":10,"max_s":20},` +
			`"aspects":["9:16"],"sections":[{"name":"S","purpose":"P","words":10}],"look":{"cards":{"end":{"title":""}}}}`,
		"an aspect not made": `{"id":"a","name":"A","use_case":"U","length":{"min_s":10,"max_s":20},` +
			`"aspects":["4:3"],"sections":[{"name":"S","purpose":"P","words":10}],"look":{"cards":{}}}`,
		"no section": `{"id":"a","name":"A","use_case":"U","length":{"min_s":10,"max_s":20},"aspects":["9:16"],` +
			`"sections":[],"look":{"cards":{}}}`,
		"no cards said": `{"id":"a","name":"A","use_case":"U","length":{"min_s":10,"max_s":20},"aspects":["9:16"],` +
			`"sections":[{"name":"S","purpose":"P","words":10}],"look":{}}`,
		"a bad look": `{"id":"a","name":"A","use_case":"U","length":{"min_s":10,"max_s":20},"aspects":["9:16"],` +
			`"sections":[{"name":"S","purpose":"P","words":10}],"look":{"cards":{},"frame":{"chrome":"tv"}}}`,
	} {
		_, err := loadTemplates(fstest.MapFS{"templates/a.json": {Data: []byte(raw)}})
		require.Error(t, err, name)
	}

	_, err = loadTemplates(fstest.MapFS{
		"templates/a.json": {Data: []byte(good)}, "templates/b.json": {Data: []byte(good)},
	})
	require.ErrorContains(t, err, "another template is a")
}

// A make with app-ad is 9:16 with captions, the saved default's captions, frame and background, and an end card
// titled with the video's title; the default is not changed, and the maker is given the card.
func TestAMakeWithATemplate(t *testing.T) {
	t.Parallel()

	r := framedRig(t)
	before := r.savedDefault(t)

	_, rev, err := r.svc.Make(t.Context(), MakeRequest{
		Take: takeID, By: "d-1", ByName: "d-1", RequestID: "a1", Template: "app-ad",
	})
	require.NoError(t, err)
	assert.Equal(t, "9:16", rev.Aspect)
	assert.True(t, rev.Captions)
	assert.Equal(t, before.Captions, rev.Look.Captions)
	assert.Equal(t, before.Frame, rev.Look.Frame)
	assert.Equal(t, before.Background, rev.Look.Background)
	assert.Equal(t, &compose.Cards{Accent: compose.DefaultAccent, End: &compose.Card{
		Title: "Tải ứng dụng ngay", Subtitle: "Đăng nhập", DurationMs: compose.DefaultEndMs,
	}}, rev.Look.Cards, "the end card: the call to action over the take's title, as aidemo's outro")
	assert.Equal(t, before, r.savedDefault(t), "a template changes no default")

	// The same request answers the same revision; the same key without the template is another request.
	_, again, err := r.svc.Make(t.Context(), MakeRequest{
		Take: takeID, By: "d-1", ByName: "d-1", RequestID: "a1", Template: "app-ad",
	})
	require.NoError(t, err)
	assert.Equal(t, rev.Rev, again.Rev)

	_, _, err = r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: "d-1", ByName: "d-1", RequestID: "a1"})
	require.ErrorIs(t, err, ErrIdempotency)

	// Classic captions, which the rig's fake maker can make.
	r.setDefault(t, Look{Captions: CaptionsLook{Preset: "legacy"}, Frame: compose.DefaultFrame(compose.ChromeAndroid),
		Background: &compose.Background{Preset: "midnight"}})
	r.runAll(t)
	require.Len(t, r.maker.inputs, 1)
	assert.Equal(t, rev.Look.Cards, r.maker.inputs[0].Cards)
	assert.Equal(t, compose.Portrait, r.maker.inputs[0].Aspect)
}

// What a make sends wins over its template, section by section: the aspect among the template's, captions, a
// background and the cards; the owner's sent sections are saved in the default, the template's are not. A later
// make titles the cards with the video's title.
func TestWhatAMakeSendsWinsOverItsTemplate(t *testing.T) {
	t.Parallel()

	r := framedRig(t)
	before := r.savedDefault(t)

	v, rev, err := r.svc.Make(t.Context(), MakeRequest{
		Take: takeID, Title: "SecondBite", By: Owner, ByName: Owner, RequestID: "o1", Template: "product-walkthrough",
		Captions: ptr(false), Look: &LookInput{Background: &BackgroundInput{Preset: ptr("ocean")}},
	})
	require.NoError(t, err)
	assert.Equal(t, "16:9", rev.Aspect, "the template's")
	assert.False(t, rev.Captions, "as sent")
	assert.Equal(t, "ocean", rev.Look.Background.Preset)
	require.NotNil(t, rev.Look.Cards)
	assert.Equal(t, "SecondBite", rev.Look.Cards.Title.Title, "the video's title")
	assert.Equal(t, int64(compose.DefaultTitleMs), rev.Look.Cards.Title.DurationMs)
	assert.Equal(t, "Dùng thử ngay", rev.Look.Cards.End.Title, "the call to action")
	assert.Equal(t, "SecondBite", rev.Look.Cards.End.Subtitle)

	saved := r.savedDefault(t)
	assert.Equal(t, "ocean", saved.Background.Preset, "the owner's sent section is saved")
	assert.Equal(t, before.Frame, saved.Frame)
	assert.Nil(t, saved.Cards)

	// A make of the video again with a template and its own cards: the aspect sent, the cards sent.
	_, next, err := r.svc.Make(t.Context(), MakeRequest{
		Take: takeID, By: Owner, ByName: Owner, RequestID: "o2", Template: "feature-announcement", Aspect: "16:9",
		Look: &LookInput{Cards: &CardsInput{End: &CardInput{Title: "Thử ngay"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, "16:9", next.Aspect)
	assert.Equal(t, "Thử ngay", next.Look.Cards.End.Title)
	assert.Nil(t, next.Look.Cards.Title)

	// A third, with the walkthrough's cards, titles them with the video's title, not the take's.
	_, third, err := r.svc.Make(t.Context(), MakeRequest{
		Take: takeID, Title: "Ignored", By: Owner, ByName: Owner, RequestID: "o3", Template: "product-walkthrough",
	})
	require.NoError(t, err)
	assert.Equal(t, v.ID, mustVideoOf(t, r).ID)
	assert.Equal(t, "SecondBite", third.Look.Cards.Title.Title)

	// The App Store's preview: its own size, no captions, the screen only, no card.
	_, store, err := r.svc.Make(t.Context(), MakeRequest{
		Take: takeID, By: Owner, ByName: Owner, RequestID: "o4", Template: "app-store-preview",
	})
	require.NoError(t, err)
	assert.Equal(t, "886x1920", store.Aspect)
	assert.False(t, store.Captions)
	assert.Equal(t, compose.ChromeFull, store.Look.Frame.Chrome)
	assert.Nil(t, store.Look.Cards)

	for name, bad := range map[string]MakeRequest{
		"an aspect not the template's": {Template: "app-ad", Aspect: "16:9"},
		"no such template":             {Template: "billboard"},
		"an aspect not made":           {Aspect: "4:3"},
	} {
		bad.Take, bad.By = takeID, Owner
		_, _, err := r.svc.Make(t.Context(), bad)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

func mustVideoOf(t *testing.T, r *rig) Video {
	t.Helper()

	v, err := r.svc.OfTake(t.Context(), takeID)
	require.NoError(t, err)

	return v
}

// A long title is cut for a card: at most its 80 characters, at a space.
func TestACardTitleIsCutAtASpace(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Đặt món", cutWords("  Đặt   món ", compose.MaxCardTitle))

	long := "Một hai ba bốn năm sáu bảy tám chín mười một hai ba bốn năm sáu bảy tám chín mười một hai"
	got := cutWords(long, compose.MaxCardTitle)
	assert.LessOrEqual(t, len([]rune(got)), compose.MaxCardTitle)
	assert.Equal(t, long[:len(got)], got, "a prefix of the title")
	assert.NotEqual(t, ' ', long[len(got)-1])
	assert.Equal(t, byte(' '), long[len(got)], "cut at a space")
}

// The templates as listed start from the saved default: its frame and background where a template sets none, the
// App Store's full frame where it does; their cards are apart, untitled.
func TestTheTemplatesListStartsFromTheDefault(t *testing.T) {
	t.Parallel()

	r := framedRig(t)
	r.setDefault(t, Look{Captions: CaptionsLook{Preset: "pop", Position: "top"},
		Frame: compose.DefaultFrame(compose.ChromeIPhone), Background: &compose.Background{Preset: "ocean"}})

	list, err := r.svc.Templates(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 5)

	ad, store := list[0], list[4]
	assert.Equal(t, "app-ad", ad.ID)
	assert.Equal(t, "pop", ad.Look.Captions.Preset)
	assert.Equal(t, compose.ChromeIPhone, ad.Look.Frame.Chrome)
	assert.Equal(t, "ocean", ad.Look.Background.Preset)
	assert.Nil(t, ad.Look.Cards)
	assert.Equal(t, &compose.Cards{Accent: compose.DefaultAccent, End: &compose.Card{
		Title: "Tải ứng dụng ngay", Subtitle: "{title}", DurationMs: 3000,
	}}, ad.Cards(), "its words as stored, {title} for the video's")

	assert.Equal(t, compose.ChromeFull, store.Look.Frame.Chrome)
	assert.Nil(t, store.Cards())
}

// A look sample with a template is resolved as a make with it: its aspect and its cards, titled; with a revision
// it is refused, as a look change takes no template.
func TestTheLookSampleOfATemplate(t *testing.T) {
	t.Parallel()

	r := framedRig(t)

	p, err := r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Template: "product-walkthrough", Card: CardEnd})
	require.NoError(t, err)
	assert.Equal(t, "16:9", p.aspect)
	assert.Equal(t, &compose.Card{Title: "Dùng thử ngay", Subtitle: "Đăng nhập", DurationMs: compose.DefaultEndMs}, p.card)

	p, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Template: "app-store-preview"})
	require.NoError(t, err)
	assert.Equal(t, "886x1920", p.aspect)
	assert.False(t, p.captions)

	_, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Template: "app-ad", Card: CardTitle})
	require.ErrorIs(t, err, ErrConflict, "app-ad has no title card")

	_, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Template: "app-ad", Rev: 1})
	require.ErrorIs(t, err, ErrInvalid)

	_, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Template: "app-ad", Aspect: "16:9"})
	require.ErrorIs(t, err, ErrInvalid)
}
