package parse

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// 最小化的貼文單元，結構取自 2026-09-20 的實際 DOM。
// 不用整頁 dump 當 fixture：那是 4MB，而且會把不相關的改版變成測試雜訊。
func article(listingID, sellerID, body, ntTag string) string {
	nt := ""
	if ntTag != "" {
		nt = `<span>NT$` + ntTag + `</span>`
	}
	return `<div data-virtualized="false">
	  <div><a href="/groups/150336012291472/user/` + sellerID + `/">賣家</a></div>
	  <div data-ad-rendering-role="story_message"><div dir="auto">` + body + `</div></div>
	  ` + nt + `
	  <a href="/commerce/listing/` + listingID + `/?ref=share_attachment&amp;__cft__[0]=abc">查看商品</a>
	</div>`
}

func parseHTML(t *testing.T, frag string) []Listing {
	t.Helper()
	doc, err := html.Parse(strings.NewReader("<html><body>" + frag + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	return Articles(doc)
}

func TestArticlesExtractsListing(t *testing.T) {
	got := parseHTML(t, article("1727621698341776", "100000285214071", "一張三百，要的請密我", "300"))
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	l := got[0]
	if l.ID != "1727621698341776" {
		t.Errorf("ID = %q", l.ID)
	}
	if l.SellerID != "100000285214071" {
		t.Errorf("SellerID = %q", l.SellerID)
	}
	if l.Price != 300 {
		t.Errorf("Price = %d, want 300（來自內文的中文數字）", l.Price)
	}
	if l.Status != StatusSelling {
		t.Errorf("Status = %q", l.Status)
	}
	if l.Permalink != "https://www.facebook.com/commerce/listing/1727621698341776/" {
		t.Errorf("Permalink = %q", l.Permalink)
	}
}

// role="article" 是 FB 的載入骨架，不是貼文。誤用它當錨點會解析出 0 則，
// 而且是靜默失敗 —— 沒有錯誤，只是永遠沒有結果。
func TestArticlesIgnoresLoadingSkeleton(t *testing.T) {
	skeleton := `<div role="article"><div aria-label="載入中……" role="status"
	   data-visualcompletion="loading-state"></div></div>`
	if got := parseHTML(t, skeleton); len(got) != 0 {
		t.Fatalf("載入骨架不應被當成貼文，實得 %d 則", len(got))
	}
}

func TestArticlesPrefersBodyOverNTTag(t *testing.T) {
	// 實際案例：徵求貼文把 NT$ 填成 9999 佔位，真實出價寫在內文
	got := parseHTML(t, article("2117614005779645", "100000140812824",
		"誠心收購 混沌邪教徒 成品佳 第11版 任務卡兩套組500 匯款後超商取貨+60元", "9,999"))
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	if got[0].Price != 500 {
		t.Errorf("Price = %d, want 500（內文優先，不可用 NT$9999）", got[0].Price)
	}
	if got[0].NTTag != 9999 {
		t.Errorf("NTTag = %d, want 9999", got[0].NTTag)
	}
	if got[0].Status != StatusWanted {
		t.Errorf("Status = %q, want wanted", got[0].Status)
	}
}

func TestArticlesFallsBackToNTTag(t *testing.T) {
	// 實際案例：內文沒有任何數字，但賣家在 NT$ 欄位填了願付價
	got := parseHTML(t, article("1648386143659993", "100000000000009",
		"誠徵！若有意釋出者請私訊我，萬分感謝～", "4,850"))
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	if got[0].Price != 4850 {
		t.Errorf("Price = %d, want 4850（內文無數字時退回 NT$）", got[0].Price)
	}
	if got[0].Status != StatusWanted {
		t.Errorf("Status = %q, want wanted", got[0].Status)
	}
}

func TestArticlesNeverFallsBackToSentinel(t *testing.T) {
	got := parseHTML(t, article("999", "100000000000009", "有人要嗎", "9,999"))
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	if got[0].Price != -1 {
		t.Errorf("Price = %d, want -1（9999 是佔位值，不可當價格）", got[0].Price)
	}
}

func TestArticlesMarksTruncated(t *testing.T) {
	got := parseHTML(t, article("1", "2", "很長的內容…… 查看更多", "100"))
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	if !got[0].Truncated {
		t.Error("含「查看更多」應標記為截斷，否則完整內文永遠不會被補抓")
	}
	if strings.Contains(got[0].Text, "查看更多") {
		t.Errorf("截斷標記不應留在內文中: %q", got[0].Text)
	}
}

func TestArticlesCapturesRawHTML(t *testing.T) {
	got := parseHTML(t, article("1", "2", "測試", "100"))
	if len(got) != 1 {
		t.Fatalf("應解析出 1 則，實得 %d", len(got))
	}
	if !strings.Contains(got[0].RawHTML, "data-virtualized") {
		t.Error("RawHTML 應保留容器本身，供解析器改版後重跑")
	}
}
