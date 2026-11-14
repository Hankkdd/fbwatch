package parse

import "testing"

// 商品卡片的骨架，取自 2026-11-08 的實際 DOM（class 省略，解析器不看 class）。
func commerceCard(body, price, title string) string {
	return `<div data-virtualized="false">
	  <div><a href="/groups/100000000000001/user/900000000000001/">賣家</a></div>
	  <div data-ad-rendering-role="story_message"><div dir="auto">` + body + `</div></div>
	  <a href="/commerce/listing/5000000000000002/" role="link"><div><div>
	    <div><span dir="auto"><span><span><div>NT$` + price + `<span><span> </span><span aria-hidden="true"> · </span></span>新北市, 台灣</div></span></span></span></div>
	    <div><span dir="auto"><span style="-webkit-line-clamp: 2;"><div>` + title + `</div></span></span></div>
	  </div></div></a>
	  <div><div aria-label="發訊息" role="button">發訊息</div></div>
	</div>`
}

func TestExtractTitleFromCommerceCard(t *testing.T) {
	got := parseHTML(t, commerceCard("售如標題，半塗 交易方式：店到店（+60）", "1,600", "售 壓克力立牌兩個"))
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	if got[0].Title != "售 壓克力立牌兩個" {
		t.Errorf("Title = %q", got[0].Title)
	}
	if got[0].NTTag != 1600 {
		t.Errorf("NTTag = %d", got[0].NTTag)
	}
}

// 內文裡以 NT$ 開頭的段落不能被當成價格列，否則下一段內文會變成標題。
func TestExtractTitleIgnoresPriceInsideBody(t *testing.T) {
	body := `<div>NT$500 含運</div><div>第二段內文</div>`
	got := parseHTML(t, commerceCard(body, "500", "限量海報 A款"))
	if got[0].Title != "限量海報 A款" {
		t.Errorf("Title = %q", got[0].Title)
	}
}

// 一般貼文沒有商品卡片，標題應為空，不能拿別的文字充數。
func TestExtractTitleEmptyForPlainPost(t *testing.T) {
	got := parseHTML(t, article("5000000000000002", "900000000000001", "徵 限量海報 A款", ""))
	if got[0].Title != "" {
		t.Errorf("Title = %q, want empty", got[0].Title)
	}
}
