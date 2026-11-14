// loadprobe 診斷 browser.LoadPage 的等待行為：
// 每秒回報一次取到的 DOM 大小、最終網址、以及關鍵字是否出現。
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"fbwatch/internal/browser"
)

func main() {
	cdp := flag.String("cdp", "http://127.0.0.1:9222", "CDP 端點")
	url := flag.String("url", "", "要載入的網址")
	key := flag.String("key", "redacted_description", "要等待的關鍵字")
	secs := flag.Int("secs", 30, "觀察秒數")
	flag.Parse()

	if *url == "" {
		log.Fatal("需要 -url")
	}

	b := rod.New().ControlURL(launcher.MustResolveURL(*cdp)).MustConnect()
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		log.Fatal(err)
	}
	defer page.Close()

	if _, err := (proto.PageNavigate{URL: *url}).Call(page); err != nil {
		log.Fatal(err)
	}
	_ = (proto.PageBringToFront{}).Call(page)

	for i := 1; i <= *secs; i++ {
		time.Sleep(time.Second)
		doc, err := browser.OuterHTML(page)
		info, _ := page.Info()
		u := ""
		if info != nil {
			u = info.URL
			if len(u) > 54 {
				u = u[:54] + "…"
			}
		}
		if err != nil {
			fmt.Printf("%2ds  OuterHTML 錯誤: %v\n", i, err)
			continue
		}
		fmt.Printf("%2ds  bytes=%-9d key=%-5v  %s\n",
			i, len(doc), strings.Contains(doc, *key), u)
	}
}
