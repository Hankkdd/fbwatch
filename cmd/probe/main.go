// probe 是開發用工具：從貼文訊息節點往上走，找出穩定的貼文容器錨點。
// FB 改版後解析器失效時，用它重新定位。
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

func attrs(n *html.Node) string {
	var parts []string
	for _, a := range n.Attr {
		if a.Key == "class" || a.Key == "style" {
			continue
		}
		v := a.Val
		if len(v) > 60 {
			v = v[:60] + "…"
		}
		parts = append(parts, a.Key+"="+v)
	}
	return strings.Join(parts, " ")
}

func countLinks(n *html.Node) (total int, kinds map[string]int) {
	kinds = map[string]int{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				total++
				switch {
				case strings.Contains(a.Val, "/commerce/listing/"):
					kinds["listing"]++
				case strings.Contains(a.Val, "/user/"):
					kinds["user"]++
				case strings.Contains(a.Val, "/posts/"), strings.Contains(a.Val, "/permalink/"):
					kinds["permalink"]++
				case strings.HasPrefix(a.Val, "?"):
					kinds["relative"]++
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return
}

func main() {
	textPat := flag.String("text", "", "改為尋找符合此正則的文字節點，並印出其祖先鏈")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "用法: probe [-text <regexp>] <dump.html>")
		os.Exit(1)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		panic(err)
	}
	defer f.Close()
	doc, err := html.Parse(f)
	if err != nil {
		panic(err)
	}

	var re *regexp.Regexp
	if *textPat != "" {
		re = regexp.MustCompile(*textPat)
	}

	parent := map[*html.Node]*html.Node{}
	var found []*html.Node
	var walk func(*html.Node, *html.Node)
	walk = func(n, p *html.Node) {
		parent[n] = p
		if re != nil {
			if n.Type == html.TextNode && re.MatchString(n.Data) {
				found = append(found, n)
			}
		} else {
			for _, a := range n.Attr {
				if a.Key == "data-ad-rendering-role" && a.Val == "story_message" {
					found = append(found, n)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, n)
		}
	}
	walk(doc, nil)

	fmt.Printf("找到 %d 個節點\n\n", len(found))
	for i, n := range found {
		fmt.Printf("===== #%d (%q) 的祖先鏈 =====\n", i+1, strings.TrimSpace(n.Data))
		cur := n
		for lvl := 0; lvl < 14 && cur != nil; lvl++ {
			total, kinds := countLinks(cur)
			fmt.Printf("  [%2d] <%s> links=%d %v  %s\n", lvl, cur.Data, total, kinds, attrs(cur))
			cur = parent[cur]
		}
		fmt.Println()
	}
}
