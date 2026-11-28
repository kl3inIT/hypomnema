package listening_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// paraphrases are more posts of vetcPosts' four stories, each told in words the others do not share, and two posts
// on another subject that name VETC in passing (issue #1306: pairs with the same story in different words). Written
// for this test, as vetcPosts were.
var paraphrases = []struct{ story, text string }{
	{"double", "Đi qua cổng thu phí tự động mà bị tính phí gấp đôi, khiếu nại VETC thì bảo chờ"},
	{"double", "Một chuyến đi mà sao kê VETC ghi hai giao dịch cho cùng một trạm"},
	{"login", "Mở ứng dụng VETC lên là báo sai mật khẩu dù nhập đúng, vào tài khoản không nổi"},
	{"login", "VETC bản mới cứ văng ra màn hình chờ, không vào được tài khoản để xem số dư"},
	{"tag", "Barie không nâng vì máy quét không nhận tem VETC trên xe, phải chuyển làn"},
	{"tag", "Con chip dán kính của VETC bị hỏng hay sao mà cổng tự động không mở cho mình"},
	{"wallet", "Đã nối VETC với ví để tự nạp khi gần hết tiền, khỏi lo quên nạp"},
	{"wallet", "Bật tính năng tự động nạp tiền từ ví vào VETC thấy khá tiện"},
	{"other", "Giá xăng hôm nay tăng, đổ đầy bình tốn hơn hẳn, chẳng liên quan gì VETC"},
	{"other", "Hỏi về bảo hiểm xe máy bắt buộc, VETC có bán không hay phải mua ngoài"},
}

// gatewayModel asks a real gateway's chat completions with a key, as the control plane's in-process call would.
func gatewayModel(base, key string) listening.Model {
	client := &http.Client{Timeout: 3 * time.Minute}

	address := strings.TrimRight(base, "/") + "/v1/chat/completions"

	return func(ctx context.Context, body []byte) ([]byte, error) {
		//nolint:gosec // G704: the tester's own gateway, named in the environment
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req) //nolint:gosec // G704: as above
		if err != nil {
			return nil, err
		}

		defer resp.Body.Close() //nolint:errcheck // read below

		out, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", listening.ErrModelNotServed, out)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the gateway answered %d: %s", resp.StatusCode, out)
		}

		return out, nil
	}
}

// TestLiveSemanticPassOnVietnamesePosts measures the semantic pass with the real models on the Vietnamese fixtures:
// the same-story pairs the clusters join, the pairs of two stories they join wrongly, where the two off-subject posts
// rank, the clusters that pass the audit, the quotes the full read kept, and the calls' tokens and seconds, first
// with the code alone, then with each model. It never runs in CI: set HYPOMNEMA_LISTENING_MODEL_URL (the gateway,
// such as https://hypomnema.vadan.app), HYPOMNEMA_LISTENING_MODEL_KEY (a gateway key that may use the models) and
// HYPOMNEMA_LISTENING_MODELS (comma-separated; the two defaults when unset). The pull request's Verification.
func TestLiveSemanticPassOnVietnamesePosts(t *testing.T) {
	base, key := os.Getenv("HYPOMNEMA_LISTENING_MODEL_URL"), os.Getenv("HYPOMNEMA_LISTENING_MODEL_KEY")
	if base == "" || key == "" {
		t.Skip("live check: set HYPOMNEMA_LISTENING_MODEL_URL and HYPOMNEMA_LISTENING_MODEL_KEY")
	}

	models := []string{listening.DefaultReviewModel, listening.DefaultReadModel}
	if m := os.Getenv("HYPOMNEMA_LISTENING_MODELS"); m != "" {
		models = strings.Split(m, ",")
	}

	type post struct{ story, text, author string }

	var posts []post

	for i, p := range vetcPosts {
		posts = append(posts, post{p.story, p.text, fmt.Sprintf("u/a%02d", i)})
	}

	for i, p := range paraphrases {
		posts = append(posts, post{p.story, p.text, fmt.Sprintf("u/b%02d", i)})
	}

	story := map[string]string{}
	at := time.Now().UTC().AddDate(0, 0, -2)
	platform := func(name string) listening.Platform {
		var items []listening.Item

		for i, p := range posts {
			if (name == "reddit") != (i%2 == 0) {
				continue
			}

			id := fmt.Sprintf("%s-%02d", name, i)
			story[p.text] = p.story
			items = append(items, listening.Item{
				NativeID: id, URL: "https://example.com/" + id, Title: p.text, Author: p.author, PublishedAt: &at,
			})
		}

		return listening.Platform{
			Name: name, Backends: []listening.Backend{{
				Name: "fixture", Host: name,
				Search: func(context.Context, listening.Request) (listening.Result, error) {
					return listening.Result{Items: items}, nil
				},
			}},
			Read: func(_ context.Context, _ listening.Request, it listening.Item) (string, error) {
				return it.Title + "\n\n[comment by u/khac, score 12]\nMình cũng gặp y như vậy tuần trước.", nil
			},
		}
	}

	p := listening.Plan{
		Topic:   "VETC",
		Sources: []string{"web", "reddit"},
		Subqueries: []listening.Subquery{
			{Query: "VETC trừ tiền hai lần"}, {Query: "VETC không đăng nhập được"}, {Query: "VETC thẻ dán không nhận"},
			{Query: "VETC nạp tiền ví"},
		},
	}

	measure := func(name, review, read string) {
		s := listening.New(listening.Config{
			Platforms: []listening.Platform{platform("web"), platform("reddit")}, ReviewModel: review, ReadModel: read,
		})

		a, err := s.Search(t.Context(), p, listening.Caller{Model: gatewayModel(base, key)})
		require.NoError(t, err)

		cluster := map[string]string{}
		for _, it := range a.Items {
			cluster[it.Title] = it.Cluster
		}

		same, joined, falseJoins := 0, 0, 0

		for i := range posts {
			for j := i + 1; j < len(posts); j++ {
				if posts[i].story == "other" || posts[j].story == "other" {
					continue
				}

				together := cluster[posts[i].text] == cluster[posts[j].text]

				if posts[i].story == posts[j].story {
					same++

					if together {
						joined++
					}
				} else if together {
					falseJoins++
				}
			}
		}

		var offRanks []int

		quotes := 0

		for i, it := range a.Items {
			if story[it.Title] == "other" {
				offRanks = append(offRanks, i+1)
			}

			quotes += len(it.Quotes)
		}

		passed := 0

		for _, c := range a.Clusters {
			if c.Audit != nil && c.Audit.Status == listening.AuditPassed {
				passed++
			}
		}

		t.Logf("%s: %d clusters, %d passed the audit; same-story pairs joined %d of %d; pairs of two stories "+
			"joined %d; off-subject posts ranked %v of %d; %d checked quotes", name, len(a.Clusters), passed, joined,
			same, falseJoins, offRanks, len(a.Items), quotes)

		for _, st := range []struct {
			name string
			step listening.Step
		}{{"rerank", a.Pass.Rerank}, {"clusters", a.Pass.Clusters}, {"read", a.Pass.Read}} {
			tokens := "unknown"
			if st.step.PromptTokens != nil {
				tokens = fmt.Sprintf("%d+%d", *st.step.PromptTokens, *st.step.CompletionTokens)
			}

			t.Logf("  %s: %s %s, %d calls, %s tokens, %.1f s; %s %s", st.name, st.step.State, st.step.Model,
				st.step.Calls, tokens, st.step.Seconds, st.step.Detail, st.step.Reason)
		}

		for _, c := range a.Clusters {
			audit := ""
			if c.Audit != nil {
				audit = fmt.Sprintf(" %s %v", c.Audit.Status, c.Audit.Failed)
			}

			var stories []string
			for _, id := range c.Items {
				for _, it := range a.Items {
					if it.ID == id {
						stories = append(stories, story[it.Title])
					}
				}
			}

			t.Logf("  %s %.1f%s %q %v", c.ID, c.Score, audit, c.Title, stories)
		}
	}

	measure("code alone", listening.ModelOff, listening.ModelOff)

	for _, m := range models {
		measure(m, m, m)
	}
}
