package listening_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/unicode/norm"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// Short Vietnamese posts about VETC, four stories, as people write them (written for this test from the kinds of
// complaints the research note's case is about; no reference has Vietnamese text).
var vetcPosts = []struct{ story, text string }{
	{"double", "VETC trừ tiền hai lần khi qua trạm BOT Long Thành, gọi tổng đài mãi không ai nghe"},
	{"double", "Bị VETC trừ tiền 2 lần ở trạm thu phí, tổng đài hẹn 7 ngày mới hoàn tiền"},
	{"double", "Ai bị trừ tiền hai lần qua trạm thu phí không dừng VETC chưa? Mình mất 2 lần phí"},
	{"double", "Tài khoản VETC tự nhiên bị trừ phí hai lần cùng một lượt qua trạm, đòi hoàn tiền rất lâu"},
	{"login", "App VETC cập nhật xong không đăng nhập được, báo lỗi hệ thống liên tục"},
	{"login", "Ứng dụng VETC lỗi đăng nhập sau bản cập nhật mới, nạp tiền cũng không được"},
	{"login", "Không đăng nhập được app VETC từ sáng, cứ báo lỗi kết nối"},
	{"tag", "Trạm không đọc được thẻ VETC dán trên kính, phải dừng lại trả tiền mặt"},
	{"tag", "Thẻ dán VETC không nhận ở làn ETC, nhân viên bắt lùi xe ra làn thu tiền mặt"},
	{"tag", "Làn ETC không nhận thẻ VETC, barrie không mở, xe sau bấm còi inh ỏi"},
	{"wallet", "VETC liên kết ví điện tử để nạp tiền tự động, có ai dùng thử chưa"},
	{"wallet", "Nạp tiền VETC qua ví điện tử giờ tự động trừ khi số dư thấp, tiện thật"},
}

// Design row "Clusters", the test that decides how items are compared for Vietnamese: lexical overlap, the words
// and character 3-grams with the topic's words taken out, or embeddings. Lexical won (cluster.go): at the threshold
// set from it, 12 of the 13 pairs of one story are alike and no pair of two stories is. last30days' own threshold,
// 0.48, finds none of them alike; obsei's model-free embedding (hashed character 3-grams, cosine) puts pairs of
// different stories above pairs of one, so no threshold separates them; and the gateway has no embedding model.
func TestVietnameseSimilarityDecidedLexical(t *testing.T) {
	t.Parallel()

	same, sameAlike, falseAlike := 0, 0, 0

	for i := range vetcPosts {
		for j := i + 1; j < len(vetcPosts); j++ {
			sim := listening.Similarity(vetcPosts[i].text, vetcPosts[j].text, "VETC")

			if vetcPosts[i].story == vetcPosts[j].story {
				same++

				if sim >= 0.20 {
					sameAlike++
				}

				assert.Less(t, sim, 0.48, "last30days' threshold joins no Vietnamese pair: %q / %q",
					vetcPosts[i].text, vetcPosts[j].text)

				continue
			}

			if sim >= 0.20 {
				falseAlike++
			}
		}
	}

	assert.Equal(t, 13, same)
	assert.GreaterOrEqual(t, sameAlike, 12)
	assert.Zero(t, falseAlike, "no two stories are alike")

	// Decomposed and composed Vietnamese are the same text.
	decomposed := norm.NFD.String("Không đăng nhập được app VETC")
	assert.InDelta(t, 1.0, listening.Similarity(decomposed, "Không đăng nhập được app VETC", ""), 1e-9)
	// Without the topic taken out, every post is a little alike through it.
	assert.Greater(t, listening.Similarity(vetcPosts[0].text, vetcPosts[7].text, ""),
		listening.Similarity(vetcPosts[0].text, vetcPosts[7].text, "VETC"))
}

// Design row "Clusters": Vietnamese posts from two sources cluster by story; a story told on one source is
// single-source, one told on both with a weak best score thin-evidence, and each item names its cluster.
func TestVietnamesePostsClusterByStory(t *testing.T) {
	t.Parallel()

	c := newClock()
	at := start.AddDate(0, 0, -1)
	items := func(source string) []listening.Item {
		var out []listening.Item

		for i, p := range vetcPosts {
			if (source == "reddit") != (i%3 == 0) {
				continue
			}

			id := source + "-" + string(rune('a'+i))
			out = append(out, listening.Item{
				NativeID: id, URL: "https://example.com/" + id, Title: p.text, PublishedAt: &at,
			})
		}

		return out
	}

	platform := func(name string) listening.Platform {
		return listening.Platform{Name: name, Backends: []listening.Backend{{
			Name: "fake", Host: name,
			Search: func(context.Context, listening.Request) (listening.Result, error) {
				return listening.Result{Items: items(name)}, nil
			},
		}}}
	}

	s := service(c, platform("web"), platform("reddit"))
	p := plan([]string{"web", "reddit"}, "VETC trừ tiền", "VETC đăng nhập", "VETC thẻ dán", "VETC ví điện tử")
	p.Topic = "VETC"
	p.MaxResults = 25

	a, err := s.Search(t.Context(), p, nil)
	require.NoError(t, err)
	require.Len(t, a.Items, len(vetcPosts))

	story := map[string]string{}
	for _, it := range a.Items {
		for _, v := range vetcPosts {
			if v.text == it.Title {
				story[it.ID] = v.story
			}
		}
	}

	clusterOf := map[string]string{}
	for _, it := range a.Items {
		require.NotEmpty(t, it.Cluster)
		clusterOf[it.ID] = it.Cluster
	}

	stories := map[string]map[string]bool{}
	for _, cl := range a.Clusters {
		require.NotEmpty(t, cl.Representatives)
		assert.LessOrEqual(t, len(cl.Representatives), 3)
		assert.Equal(t, cl.Items[0], cl.Representatives[0], "the best item represents first")

		for _, id := range cl.Items {
			assert.Equal(t, cl.ID, clusterOf[id])

			if stories[cl.ID] == nil {
				stories[cl.ID] = map[string]bool{}
			}

			stories[cl.ID][story[id]] = true
		}

		switch {
		case len(cl.Sources) == 1:
			assert.Equal(t, listening.SingleSource, cl.Uncertainty)
		case cl.Score < 55:
			assert.Equal(t, listening.ThinEvidence, cl.Uncertainty)
		default:
			assert.Empty(t, cl.Uncertainty)
		}
	}

	for id, set := range stories {
		assert.Len(t, set, 1, "cluster %s mixes stories %v", id, set)
	}

	assert.LessOrEqual(t, len(a.Clusters), 6, "the twelve posts of four stories fall into few clusters")

	multi := 0
	for _, cl := range a.Clusters {
		if len(cl.Sources) > 1 {
			multi++
		}
	}

	assert.Positive(t, multi, "a story told on both sources is one cluster with both")
}
