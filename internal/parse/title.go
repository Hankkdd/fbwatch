package parse

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// maxTitleRunes 擋掉錨點錯位時抓到的大段文字。FB 的商品標題上限是 100 字。
const maxTitleRunes = 150

// ExtractTitle 取出商品卡片上的標題。
//
// 很多賣家的內文只寫「售如標題」，品名只在標題裡。標題不在 story_message 裡，
// 而是在附件卡片上，緊接在「NT$1,600 · 新北市」那一行後面：
//
//	<div>                      ← 價格列（往上爬到第一個有下一個兄弟的祖先）
//	  …<div>NT$1,600 · 新北市</div>
//	</div>
//	<div>…<div>售 壓克力立牌兩個</div></div>   ← 標題
//
// class 都是混淆過的、每次改版都會換，所以錨點用的是文字：最深的、以 NT$ 開頭的節點。
// 內文裡也可能有以 NT$ 開頭的段落，story_message 整棵子樹不列入搜尋。
//
// 找不到就回傳空字串。一般貼文（不是商品）本來就沒有標題；
// 「免費」的商品沒有 NT$ 列，也會落空 —— 這兩種都不值得多寫一條規則。
func ExtractTitle(n *html.Node) string {
	price := deepestPriceLine(n)
	if price == nil {
		return ""
	}
	for p := price; p != nil && p != n; p = p.Parent {
		sib := nextElement(p)
		if sib == nil {
			continue
		}
		t := normalize(text(sib))
		if t == "" || strings.HasPrefix(t, "NT$") || t == "發訊息" {
			return ""
		}
		if utf8.RuneCountInString(t) > maxTitleRunes {
			return ""
		}
		return t
	}
	return ""
}

// deepestPriceLine 回傳文字以 NT$ 開頭的最深元素。
//
// 要最深的：外層容器的文字也以 NT$ 開頭（價格列在標題前面），
// 從外層開始找兄弟會跳過標題。
func deepestPriceLine(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && attr(n, "data-ad-rendering-role") == "story_message" {
		return nil
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := deepestPriceLine(c); r != nil {
			return r
		}
	}
	if n.Type == html.ElementNode && strings.HasPrefix(normalize(text(n)), "NT$") {
		return n
	}
	return nil
}

func nextElement(n *html.Node) *html.Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode {
			return s
		}
	}
	return nil
}
