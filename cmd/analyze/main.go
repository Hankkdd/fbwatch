// analyze 讀一份已存的 DOM dump，印出解析結果。
// 對檔案操作，不會再對 FB 發任何請求。
package main

import (
	"fmt"
	"os"

	"golang.org/x/net/html"

	"fbwatch/internal/parse"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: analyze <dump.html>")
		os.Exit(1)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()

	doc, err := html.Parse(f)
	if err != nil {
		panic(err)
	}

	items := parse.Articles(doc)
	fmt.Printf("找到 %d 則\n\n", len(items))
	for i, it := range items {
		txt := it.Text
		if len(txt) > 400 {
			txt = txt[:400] + "…"
		}
		posted := "(無)"
		if !it.PostedAt.IsZero() {
			posted = it.PostedAt.Format("01-02 15:04")
		}
		fmt.Printf("--- [%d] id=%s price=%d nt=%d status=%s trunc=%v posted=%s seller=%s\n", i+1, it.ID, it.Price, it.NTTag, it.Status, it.Truncated, posted, it.SellerID)
		fmt.Printf("    %s\n", it.Permalink)
		fmt.Printf("    %s\n\n", txt)
	}
}
