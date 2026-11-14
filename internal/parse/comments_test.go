package parse

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// 結構照真實詳情頁縮減：同一串留言渲染兩次、回覆巢狀在留言的 article 裡。
const commentsFixture = `<html><body>
<div aria-label="回應賣家甲的貼文，4則留言"></div>
<div class="feed">THREAD</div>
<div role="dialog">THREAD</div>
</body></html>`

const commentsThread = `
<div role="article" aria-label="買家乙的留言3小時前">
  <span dir="auto">買家乙</span><a><span dir="auto">3小時</span></a>
  <div dir="auto">第二組還在嗎？可面交</div>
  <div role="article" aria-label="賣家甲回覆買家乙的留言2小時前">
    <span dir="auto">賣家甲</span><a><span dir="auto">2小時</span></a>
    <div dir="auto">抱歉，已經有人訂了</div>
    <div role="article" aria-label="買家乙回覆賣家甲的回覆1小時前">
      <span dir="auto">買家乙</span><a><span dir="auto">1小時</span></a>
      <div dir="auto">好的，謝謝</div>
    </div>
  </div>
</div>
<div role="article" aria-label="買家丙的留言5分鐘前">
  <span dir="auto">買家丙</span><a><span dir="auto">5分鐘</span></a>
</div>`

func TestParseComments(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(strings.ReplaceAll(commentsFixture, "THREAD", commentsThread)))
	if err != nil {
		t.Fatal(err)
	}
	got := ParseComments(doc)
	if got.Total != 4 {
		t.Errorf("Total = %d, want 4", got.Total)
	}
	want := []Comment{
		{Author: "買家乙", When: "3小時", Text: "第二組還在嗎？可面交"},
		{Author: "賣家甲", ReplyTo: "買家乙", When: "2小時", Text: "抱歉，已經有人訂了"},
		{Author: "買家乙", ReplyTo: "賣家甲", When: "1小時", Text: "好的，謝謝"},
		{Author: "買家丙", When: "5分鐘", Text: "（沒有文字，可能是貼圖或照片）"},
	}
	if len(got.List) != len(want) {
		t.Fatalf("got %d comments: %+v", len(got.List), got.List)
	}
	for i := range want {
		if got.List[i] != want[i] {
			t.Errorf("comment %d = %+v, want %+v", i, got.List[i], want[i])
		}
	}
}

func TestParseCommentsNone(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div role="article" aria-label="某人的貼文"><span dir="auto">售 限量海報</span></div>`))
	got := ParseComments(doc)
	if len(got.List) != 0 || got.Total != -1 {
		t.Errorf("got %+v", got)
	}
}
