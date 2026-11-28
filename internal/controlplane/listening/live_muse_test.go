package listening_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
)

// TestLiveMuseLinksAgainstThreads checks, against the real threads.com, the ten Threads posts Muse gave in the probe
// of 2026-11-27 (muse.db, thread 42a947cb-a57e-46ea-b39c-16c6c379132a, its second answer): their authors, dates,
// quotes and kinds as Muse wrote them, in the template's labels, and their links as Muse gave them, wrapped by
// l.meta.ai. It logs what each check found. It never runs in CI: set HYPOMNEMA_LISTENING_LIVE=1 (issue #1292, the
// pull request's Verification).
func TestLiveMuseLinksAgainstThreads(t *testing.T) {
	if os.Getenv("HYPOMNEMA_LISTENING_LIVE") == "" {
		t.Skip("live check: set HYPOMNEMA_LISTENING_LIVE=1 to open the real Threads posts")
	}

	posts := []struct{ handle, code, date, from, quote, likes string }{
		{"tranthanhsang208", "DfI6avxkpVr", "2026-10-31", "image_text", "PHÍ QUẢN LÝ TÀI KHOẢN 6.600đ/tháng", "none"},
		{"tuong_2904", "DfO9aflAQCJ", "2026-11-02", "caption", "Chào mừng em về vs đội của a,", "none"},
		{"may.yas.7", "DfUDboCIDQu", "2026-11-04", "comment", "Tr oi, xài Cốc Cốc đi bà", "2,9K"},
		{"minhtiensg", "DfUomw4iepN", "2026-11-04", "caption", `App VETC vừa gửi thông báo: "CSGT kiểm tra giấy ` +
			`tờ❗", mời mua bảo hiểm trách nhiệm dân sự, tặng kèm cứu hộ 24/7. CSGT kiểm tra giấy tờ là việc thường ` +
			`ngày, theo kế hoạch tuần tra, kiểm soát, sao phải lấy hình ảnh CSGT ra doạ để bán bảo hiểm?`, "none"},
		{"cuongpham80571", "Dfaz9UAj1IE", "2026-11-07", "image_text", "VETC/Ecom/CashIn", "none"},
		{"coccoc.official", "Dfe40MVAZjj", "2026-11-08", "caption", "Vào web đọc truyện mà quên mở bằng Cốc Cốc 🤡",
			"75"},
		{"lalamwg123", "Dftgogyk8j4", "2026-11-14", "image_text", "DÁN THẺ eTag VETC Nhanh chóng - Tiện lợi - An toàn",
			"none"},
		{"coccoc.official", "DfwWTcnFPJg", "2026-11-15", "image_text", "Chặn QC", "63"},
		{"hoangvangiangmmo", "DfzcvRACPRj", "2026-11-16", "image_text", "Cài đặt FBnumber trên Cốc Cốc", "3"},
		{"vnptvinaphone.official", "DfzGAOrE0Bv", "2026-11-16", "image_text", "NHẬN ƯU ĐÃI VETC", "3"},
	}

	var (
		text  strings.Builder
		links []string
	)

	for i, p := range posts {
		text.WriteString(block(i+1, "Bài đăng", "@"+p.handle, "@"+p.handle, p.date, p.from, p.quote, p.likes))
		links = append(links, metaLink(threadsURL(p.handle, p.code)))
	}

	m := &fakeMuse{answer: func(int, muse.Question) (muse.Result, error) {
		return answeredWith(text.String(), links...), nil
	}}
	asOf := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)
	s := listening.New(listening.Config{
		Platforms: listening.MuseSources(m, &http.Client{Timeout: 20 * time.Second}),
		Now:       func() time.Time { return asOf },
	})

	got, err := s.Search(t.Context(), listening.Plan{
		Topic: "Cốc Cốc VETC", Sources: []string{listening.SourceThreads},
		Subqueries: []listening.Subquery{{Query: "Cốc Cốc VETC VMAP"}},
	}, nil)
	require.NoError(t, err)

	for _, c := range got.Coverage {
		for _, st := range c.Streams {
			for _, at := range st.Tried {
				t.Logf("coverage %s %s: %s %s", c.Source, at.Backend, at.Result, at.Detail)
			}
		}
	}

	verified := map[string]bool{}

	for _, it := range got.Items {
		verified[it.NativeID] = true
		t.Logf("item %s %s %s", it.URL, it.QuoteFrom, it.Check)
	}

	for _, it := range got.Unverified {
		t.Logf("lead %s %s %s", it.URL, it.QuoteFrom, it.Check)
	}

	assert.True(t, verified["DfUomw4iepN"], "the probe found this caption on its page")
	assert.True(t, verified["Dfe40MVAZjj"], "the probe found this caption on its page")
}
