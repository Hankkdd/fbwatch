package parse

import (
	"testing"
	"time"
)

func TestParseRelativeTime(t *testing.T) {
	now := time.Date(2026, 9, 20, 18, 0, 0, 0, time.Local)
	cases := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"3分鐘", now.Add(-3 * time.Minute), true},
		{"6小時", now.Add(-6 * time.Hour), true},
		{"2天", now.Add(-48 * time.Hour), true},
		{"1週", now.Add(-7 * 24 * time.Hour), true},
		{"剛剛", now, true},
		{" 45分鐘 ", now.Add(-45 * time.Minute), true},

		// 不是時間的字串不能誤判 —— 貼文裡常有「500」「第11版」這類數字
		{"500", time.Time{}, false},
		{"第11版", time.Time{}, false},
		{"", time.Time{}, false},
		{"3分鐘前留言", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := ParseRelativeTime(c.in, now)
		if ok != c.ok {
			t.Errorf("ParseRelativeTime(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && !got.Equal(c.want) {
			t.Errorf("ParseRelativeTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// 時間戳記的文字不在貼文卡片裡：卡片內的時間連結用 aria-labelledby
// 指向 body 底下的隱藏節點。弄錯方向（改看祖先）會靜默地抓不到時間。
func TestArticlesResolvesPostedAtViaAriaRef(t *testing.T) {
	now := time.Date(2026, 9, 20, 18, 0, 0, 0, time.Local)
	frag := `<div data-virtualized="false">
	   <div><a href="/groups/1/user/42/">賣家</a></div>
	   <a href="/commerce/listing/777/" aria-labelledby="ts1">時間</a>
	   <div data-ad-rendering-role="story_message"><div dir="auto">測試貼文</div></div>
	 </div>
	 <div hidden="true"><span id="ts1">25分鐘</span></div>`

	doc := mustParse(t, frag)
	got := ArticlesAt(doc, now)
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	want := now.Add(-25 * time.Minute)
	if !got[0].PostedAt.Equal(want) {
		t.Errorf("PostedAt = %v, want %v", got[0].PostedAt, want)
	}
}

func TestArticlesPostedAtZeroWhenAbsent(t *testing.T) {
	now := time.Date(2026, 9, 20, 18, 0, 0, 0, time.Local)
	doc := mustParse(t, article("1", "2", "沒有時間戳記", "100"))
	got := ArticlesAt(doc, now)
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	if !got[0].PostedAt.IsZero() {
		t.Errorf("抓不到時間時應為零值，實得 %v", got[0].PostedAt)
	}
}
