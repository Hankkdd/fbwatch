// Package parse 把社團動態的 DOM 轉成 listing。
//
// 定位方式分三層，每一層都是踩過坑才加上的：
//  1. 範圍限定在 role="feed" 之內 —— Messenger 的聊天列表在它外面
//  2. 單元錨點用 data-virtualized —— role="article" 是載入骨架不是貼文
//  3. 必須有 listing／post permalink —— 沒有的就不是社團貼文
package parse

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type Listing struct {
	ID        string // commerce listing id 或 post id
	Kind      string // "listing" | "post"
	Permalink string
	SellerID  string
	Text      string // story_message 子樹；feed 上是截斷版
	Truncated bool   // 內文被 FB 截斷，完整版要另外請求 permalink
	Price     int    // 由內文判定，-1 表示沒抓到
	NTTag     int    // 結構化 NT$ 欄位，不可信，僅作輔助訊號
	Status    Status
	PostedAt  time.Time // FB 的相對時間換算而來；零值表示抓不到
	RawHTML   string    // 容器本身的 HTML，供解析器改版後重跑
}

var (
	reListing = regexp.MustCompile(`/commerce/listing/(\d+)`)
	rePost    = regexp.MustCompile(`/groups/\d+/(?:posts|permalink)/(\d+)`)
	reUser    = regexp.MustCompile(`/groups/\d+/user/(\d+)`)
	rePriceNT = regexp.MustCompile(`NT\$\s*([\d,]+)`)
	rePrice   = regexp.MustCompile(`(?:＄|\$)\s*([\d,]+)|([\d,]+)\s*元`)
	reSpace   = regexp.MustCompile(`\s+`)
)

func toInt(s string) int {
	v, err := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	if err != nil {
		return -1
	}
	return v
}

// Articles 取出動態牆上的貼文。
//
// 錨點是 data-virtualized —— FB 的虛擬列表用它標記每個貼文單元。
// 不要用 role="article"：那些是尚未載入的骨架佔位符，不是真實貼文。
func Articles(doc *html.Node) []Listing {
	return ArticlesAt(doc, time.Now())
}

// ArticlesAt 與 Articles 相同，但以指定的時間換算相對時間戳記。測試用。
func ArticlesAt(doc *html.Node, now time.Time) []Listing {
	idx := buildLabelIndex(doc) // 時間戳記的隱藏節點在 feed 之外，索引要用整份文件

	// 只在動態牆容器裡面找。data-virtualized 是 FB 給所有虛擬列表的通用標記，
	// 不是「這是貼文」的意思 —— Messenger 的聊天訊息列表也帶它，
	// 實測因此把一段私人對話當成貼文抓走。範圍限定是比事後過濾更根本的防線。
	root := doc
	if feed := findAttr(doc, "role", "feed"); feed != nil {
		root = feed
	}

	var out []Listing
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && hasAttr(n, "data-virtualized") {
			if l, ok := fromArticle(n); ok {
				// 時間戳記的文字不在卡片裡 —— 卡片內的時間連結用 aria-labelledby
				// 指向 body 底下的隱藏節點，可見文字由無障礙名稱呈現。
				if t, ok := resolvePostedAt(collectLabelRefs(n), idx, now); ok {
					l.PostedAt = t
				}
				out = append(out, l)
			}
			return // 不進入巢狀單元，避免重複
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// collectLabelRefs 蒐集子樹裡所有 aria-labelledby / aria-describedby 引用的 id。
func collectLabelRefs(n *html.Node) string {
	var ids []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, k := range [...]string{"aria-labelledby", "aria-describedby"} {
				if v := attr(n, k); v != "" {
					ids = append(ids, v)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(ids, " ")
}

// buildLabelIndex 建立 id → 文字 的對照表。
func buildLabelIndex(doc *html.Node) labelIndex {
	idx := labelIndex{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id := attr(n, "id"); id != "" {
				if t := normalize(text(n)); t != "" && len(t) < 40 {
					idx[id] = t
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return idx
}

func findAttr(n *html.Node, key, val string) *html.Node {
	if n.Type == html.ElementNode && attr(n, key) == val {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findAttr(c, key, val); r != nil {
			return r
		}
	}
	return nil
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func fromArticle(n *html.Node) (Listing, bool) {
	l := Listing{Price: -1, Kind: "post"}

	// 內文只取 story_message 子樹。整張卡片含大量圖示與輔助標籤文字，會污染內容。
	if msg := findAttr(n, "data-ad-rendering-role", "story_message"); msg != nil {
		l.Text = normalize(text(msg))
	}
	full := normalize(text(n))
	if l.Text == "" {
		l.Text = full
	}

	for _, href := range hrefs(n) {
		if m := reListing.FindStringSubmatch(href); m != nil && l.ID == "" {
			l.ID, l.Kind, l.Permalink = m[1], "listing", "https://www.facebook.com/commerce/listing/"+m[1]+"/"
		}
		if m := rePost.FindStringSubmatch(href); m != nil && l.ID == "" {
			l.ID = m[1]
			l.Permalink = "https://www.facebook.com" + strings.SplitN(href, "?", 2)[0]
		}
		if m := reUser.FindStringSubmatch(href); m != nil && l.SellerID == "" {
			l.SellerID = m[1]
		}
	}

	// NT$ 欄位只作輔助訊號 —— 徵求貼文常填 9999 這類佔位值。
	if m := rePriceNT.FindStringSubmatch(full); m != nil {
		l.NTTag = toInt(m[1])
	} else {
		l.NTTag = -1
	}

	l.Truncated = strings.Contains(l.Text, "查看更多") || strings.Contains(l.Text, "……")
	l.Text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l.Text), "查看更多"))
	l.Text = strings.TrimSpace(strings.TrimSuffix(l.Text, "……"))

	l.Price = ExtractPrice(l.Text)
	// 內文為主，但內文沒有數字時退回 NT$ 欄位 —— 徵求貼文常只在欄位填願付價。
	// 9999 是已知的佔位值，不能當價格用。
	if l.Price <= 0 && l.NTTag > 0 && l.NTTag != WantedSentinel {
		l.Price = l.NTTag
	}
	l.Status = DetectStatus(l.Text, l.NTTag)

	// 只存容器本身的 HTML。整頁 4MB×每日近千次輪詢不可行，而重新解析只需要這塊。
	var buf strings.Builder
	if err := html.Render(&buf, n); err == nil {
		l.RawHTML = buf.String()
	}

	// 必須有 listing／post ID 才算數。
	//
	// 「有文字就算數」會外洩隱私：Messenger 的聊天視窗也是虛擬列表、
	// 同樣帶 data-virtualized，實測把一段私人對話當成貼文抓走並送進 Discord。
	// 沒有 permalink 的東西就不是社團貼文。
	//
	// 代價是 permalink 尚未渲染的貼文這一輪會被漏掉，下一輪再抓 ——
	// 漏一輪遠比洩漏私人內容便宜。
	return l, l.ID != ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hrefs(n *html.Node) []string {
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			if h := attr(n, "href"); h != "" {
				out = append(out, h)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		// svg 內含 <title>Facebook</title> 之類的圖示標題，會污染內文
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "svg") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func normalize(s string) string {
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}
