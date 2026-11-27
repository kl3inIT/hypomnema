package videos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The cards of a look (issue #1230, row 1): a section sent with the references' defaults, kept by a look change
// that does not send it, taken away by one sent with no card, never saved in the default, and given to the maker.

func TestTheCardsOfALook(t *testing.T) {
	t.Parallel()

	r := framedRig(t)

	// Classic captions, which the rig's fake maker can make (videos_test.go).
	v, rev, err := r.ownerMake(t, "", &LookInput{
		Captions: &CaptionsInput{Preset: ptr(captions.Legacy)},
		Cards: &CardsInput{
			Title: &CardInput{Title: "  SecondBite ", Subtitle: ptr("Đặt món trong 30 giây")},
			End:   &CardInput{Title: "Tải SecondBite", CTAURL: ptr("secondbite.vn"), DurationMs: ptr(int64(4000))},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, &compose.Cards{
		Accent: "#6c8cff",
		Title:  &compose.Card{Title: "SecondBite", Subtitle: "Đặt món trong 30 giây", DurationMs: 2600},
		End:    &compose.Card{Title: "Tải SecondBite", CTAURL: "secondbite.vn", DurationMs: 4000},
	}, rev.Look.Cards, "aidemo's accent and 2.6 s, agent-demo-video's 3.0 s unless sent")
	assert.Nil(t, r.savedDefault(t).Cards, "a make's cards are not saved in the default")

	r.runAll(t)
	require.Len(t, r.maker.inputs, 1)
	assert.Equal(t, rev.Look.Cards, r.maker.inputs[0].Cards, "the maker draws them")
	assert.Equal(t, *rev.Look.Background, r.maker.inputs[0].Background, "on the look's background")

	// A look change that sends no cards keeps them; one that sends a section with none takes them away.
	_, kept, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "l1", Look: &LookInput{
		Background: &BackgroundInput{Preset: ptr("ocean")},
	}})
	require.NoError(t, err)
	assert.Equal(t, rev.Look.Cards, kept.Look.Cards)

	_, gone, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "l2", Look: &LookInput{
		Cards: &CardsInput{},
	}})
	require.NoError(t, err)
	assert.Nil(t, gone.Look.Cards)

	// The next make, the owner's or an agent's, opens on no card.
	_, next, err := r.ownerMake(t, "", nil)
	require.NoError(t, err)
	assert.Nil(t, next.Look.Cards)

	for name, bad := range map[string]*CardsInput{
		"no title":   {Title: &CardInput{Title: " "}},
		"too short":  {End: &CardInput{Title: "A", DurationMs: ptr(int64(400))}},
		"too long":   {End: &CardInput{Title: "A", DurationMs: ptr(int64(10_001))}},
		"an accent":  {Accent: ptr("blue"), Title: &CardInput{Title: "A"}},
		"two lines":  {Title: &CardInput{Title: "A", Subtitle: ptr("B\nC")}},
		"long title": {Title: &CardInput{Title: string(make([]rune, 81))}},
	} {
		_, _, err := r.ownerMake(t, "", &LookInput{Cards: bad})
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

// The look sample of a card is the card on the look's background, in its accent; a look without that card is
// refused, and so is a card that is neither title nor end.
func TestTheLookSampleOfACard(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := realRig(t, tools)
	r.setDefault(t, DefaultLook())
	r.setTake(t, []devicetool.TakeSegment{writeSegment(t, tools, r.takes.dir, "seg-001.mp4", 576, 1280,
		span{"red", 2000})}, devicetool.TakeStep{AtMs: 500, Text: "Một hai"})

	look := &LookInput{
		Background: &BackgroundInput{Colors: &[]string{"#103020", "#103020"}},
		Cards: &CardsInput{Accent: ptr("#ff3366"), End: &CardInput{
			Title: "Tải SecondBite", CTAURL: ptr("secondbite.vn"),
		}},
	}

	px := r.sample(t, tools, PreviewRequest{Take: takeID, Look: look, Card: CardEnd})
	assert.True(t, within(pixelAt(px, 720, 10, 10), [3]int{0x10, 0x30, 0x20}, 16), "the look's background, not %v",
		pixelAt(px, 720, 10, 10))
	assert.False(t, within(pixelAt(px, 720, 360, 1100), [3]int{255, 0, 0}, 60), "not the take's picture")

	pink := 0

	for i := 0; i+2 < len(px); i += 3 {
		if within([3]int{int(px[i]), int(px[i+1]), int(px[i+2])}, [3]int{0xff, 0x33, 0x66}, 40) {
			pink++
		}
	}

	assert.Greater(t, pink, 200, "the URL in the accent")

	_, err := r.svc.Preview(t.Context(), PreviewRequest{Take: takeID, Look: look, Card: CardTitle})
	require.ErrorIs(t, err, ErrConflict, "the look has no title card")

	_, err = r.svc.Preview(t.Context(), PreviewRequest{Take: takeID, Look: look, Card: "middle"})
	require.ErrorIs(t, err, ErrInvalid)

	// The sample of a card draws that card alone: the other one, turned on and not titled yet, is not refused
	// (the review of #1242); a make with it is.
	both := &LookInput{Cards: &CardsInput{Title: &CardInput{Title: ""}, End: look.Cards.End}}
	_, err = r.svc.Preview(t.Context(), PreviewRequest{Take: takeID, Look: both, Card: CardEnd})
	require.NoError(t, err)

	_, err = r.svc.Preview(t.Context(), PreviewRequest{Take: takeID, Look: both, Card: CardTitle})
	require.ErrorIs(t, err, ErrInvalid, "the card drawn is checked")

	_, _, err = r.ownerMake(t, "", both)
	require.ErrorIs(t, err, ErrInvalid)
}
