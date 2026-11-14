package parse

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Comment 是貼文詳情頁上的一則留言或回覆。
type Comment struct {
	Author  string
	ReplyTo string // 回覆誰；頂層留言是空字串
	When    string // FB 顯示的相對時間，例如「14小時」
	Text    string
}

// Comments 是貼文頁上看得到的留言。Total 是 FB 標示的留言總數（拿不到是 -1），
// 留言多時頁面只會先展開一部分，len(List) 可能小於 Total。
type Comments struct {
	List  []Comment
	Total int
}

// 留言的 aria-label：「王小明的留言14小時前」「陳大華回覆王小明的留言14小時前」
// 「王小明回覆陳大華的回覆14小時前」。名字裡剛好有「回覆」的人會被拆錯，
// 但作者還是對的（取到第一個「回覆」之前），只影響 ReplyTo。
var reCommentLabel = regexp.MustCompile(`^(.+?)(?:回覆(.+?))?的(?:留言|回覆)\s*(.*?)前?$`)

// 「回應王小明的貼文，5則留言」
var reCommentTotal = regexp.MustCompile(`(\d+)\s*則留言`)

// ParseComments 從詳情頁 DOM 取出留言。
//
// 用畫面上的 role="article" 而不是內嵌 JSON：JSON 裡同一則留言會出現好幾份，
// 作者、時間、內文分散在不同物件，配對容易錯；畫面上每則留言自成一個 article，
// 標籤固定是「誰的留言」「誰回覆誰的留言」。
// 頁面會把同一串留言渲染兩次（主頁與對話框），用作者加內文去重。
func ParseComments(doc *html.Node) Comments {
	out := Comments{Total: -1}
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			label := attr(n, "aria-label")
			if out.Total < 0 {
				if m := reCommentTotal.FindStringSubmatch(label); m != nil && strings.Contains(label, "貼文") {
					out.Total, _ = strconv.Atoi(m[1])
				}
			}
			if attr(n, "role") == "article" {
				if c, ok := commentFrom(n, label); ok {
					key := c.Author + "\x00" + c.Text
					if !seen[key] {
						seen[key] = true
						out.List = append(out.List, c)
					}
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	return out
}

func commentFrom(n *html.Node, label string) (Comment, bool) {
	m := reCommentLabel.FindStringSubmatch(label)
	if m == nil {
		return Comment{}, false
	}
	c := Comment{Author: strings.TrimSpace(m[1]), ReplyTo: strings.TrimSpace(m[2]), When: strings.TrimSpace(m[3])}
	// 留言裡的文字區塊依序是：作者名、時間、內文（可能分成好幾段）。
	// 巢狀的 article 是底下的回覆，各自會被當成一則，這裡跳過。
	var blocks []string
	var collect func(*html.Node)
	collect = func(x *html.Node) {
		if x != n && x.Type == html.ElementNode && attr(x, "role") == "article" {
			return
		}
		if x.Type == html.ElementNode && attr(x, "dir") == "auto" {
			if t := strings.TrimSpace(text(x)); t != "" {
				blocks = append(blocks, t)
			}
			return
		}
		for ch := x.FirstChild; ch != nil; ch = ch.NextSibling {
			collect(ch)
		}
	}
	collect(n)
	var body []string
	for i, b := range blocks {
		if i == 0 && b == c.Author {
			continue
		}
		if c.When != "" && strings.HasPrefix(c.When, b) && len(body) == 0 {
			continue
		}
		body = append(body, b)
	}
	c.Text = strings.Join(body, "\n")
	if c.Text == "" {
		// 只有貼圖或照片的留言
		c.Text = "（沒有文字，可能是貼圖或照片）"
	}
	return c, true
}
