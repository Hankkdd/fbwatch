package notify

import (
	"strings"
	"testing"
)

func TestPostMessageStartsWithBodyNotURL(t *testing.T) {
	msg := PostMessage(Post{
		Body:      "售 限量版海報 1500",
		Permalink: "https://www.facebook.com/groups/123/posts/456/",
	})
	want := "售 限量版海報 1500\n<https://www.facebook.com/groups/123/posts/456/>"
	if msg != want {
		t.Fatalf("got %q, want %q", msg, want)
	}
}

func TestPostMessageKeepsHighlightFirst(t *testing.T) {
	msg := PostMessage(Post{
		Highlight: "**[限量品出現了]**",
		Body:      "售 海報 1500",
		Permalink: "https://fb.com/p/1",
	})
	lines := strings.Split(msg, "\n")
	if lines[0] != "**[限量品出現了]**" {
		t.Fatalf("第一行應該是關鍵字提示，得到 %q", lines[0])
	}
	if lines[len(lines)-1] != "<https://fb.com/p/1>" {
		t.Fatalf("最後一行應該是網址，得到 %q", lines[len(lines)-1])
	}
}

func TestPostMessageWithoutBodyIsJustLink(t *testing.T) {
	msg := PostMessage(Post{Body: "   \n ", Permalink: "https://fb.com/p/2"})
	if msg != "<https://fb.com/p/2>" {
		t.Fatalf("空內文應該只留網址，得到 %q", msg)
	}
}

func TestPostMessageWrapsURLToSuppressPreview(t *testing.T) {
	msg := PostMessage(Post{Body: "x", Permalink: "https://fb.com/p/3"})
	if !strings.Contains(msg, "<https://fb.com/p/3>") {
		t.Fatalf("網址要用 <> 包住才不會展開預覽卡片，得到 %q", msg)
	}
}

// 長內文被切段時，網址必須完整留在某一則裡 —— 切在網址中間連結就廢了。
func TestPostMessageSurvivesSplitIntact(t *testing.T) {
	body := strings.Repeat("限量版海報 1500\n", 400)
	msg := PostMessage(Post{Body: body, Permalink: "https://fb.com/p/4"})
	parts := Split(msg, MaxMessage)
	if len(parts) < 2 {
		t.Fatalf("這個長度應該要切成多則，只得到 %d 則", len(parts))
	}
	last := parts[len(parts)-1]
	if !strings.Contains(last, "<https://fb.com/p/4>") {
		t.Fatalf("網址沒有完整留在最後一則: %q", last[max(0, len(last)-120):])
	}
}

// 內文只寫「售如標題」時，標題要出現在通知裡，否則根本看不出在賣什麼。
func TestPostMessageShowsTitleBeforeBody(t *testing.T) {
	msg := PostMessage(Post{
		Title:     "售 壓克力立牌兩個",
		Body:      "售如標題，半塗",
		Permalink: "https://fb.com/p/4",
	})
	want := "**售 壓克力立牌兩個**\n售如標題，半塗\n<https://fb.com/p/4>"
	if msg != want {
		t.Fatalf("got %q, want %q", msg, want)
	}
}

func TestPostMessageSkipsTitleAlreadyInBody(t *testing.T) {
	msg := PostMessage(Post{
		Title:     "售 限量版海報",
		Body:      "售 限量版海報 1500",
		Permalink: "https://fb.com/p/5",
	})
	if strings.Count(msg, "限量版海報") != 1 {
		t.Fatalf("標題已在內文裡，不該重複出現：%q", msg)
	}
}

func TestWithMentionGoesOnFirstLine(t *testing.T) {
	got := WithMention("▌**限量品出現了**\n售 限量海報\n<https://fb.com/p/1>", "42")
	want := "▌**限量品出現了** <@42>\n售 限量海報\n<https://fb.com/p/1>"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if WithMention("x", "") != "x" {
		t.Fatal("沒有 ID 時不該改動訊息")
	}
}
