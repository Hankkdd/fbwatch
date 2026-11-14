// dumpdom 附掛到已在執行的 Chrome，載入指定網址，把渲染後的 DOM 存檔。
//
// 刻意不呼叫 Runtime.enable —— 那是 CDP 最主要的偵測向量。
// 取 DOM 只用 DOM.getDocument + DOM.getOuterHTML，全程不注入 JS。
//
// 絕對不要關閉 browser，只關自己開的分頁。那個 Chrome 持有登入態。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

var (
	reArticle = regexp.MustCompile(`role="article"`)
	rePrice   = regexp.MustCompile(`(?:NT\$|\$|＄)\s*[\d,]+|[\d,]+\s*元`)
	rePost    = regexp.MustCompile(`/(?:posts|marketplace/item)/(\d+)`)
)

// outerHTML 取渲染後的 DOM。刻意不用 Runtime.evaluate —— 不注入任何 JS。
func outerHTML(page *rod.Page) (string, error) {
	doc, err := proto.DOMGetDocument{Depth: gson.Int(-1), Pierce: true}.Call(page)
	if err != nil {
		return "", err
	}
	res, err := proto.DOMGetOuterHTML{NodeID: doc.Root.NodeID}.Call(page)
	if err != nil {
		return "", err
	}
	return res.OuterHTML, nil
}

func main() {
	cdp := flag.String("cdp", "http://127.0.0.1:9222", "已執行中 Chrome 的 CDP 端點")
	target := flag.String("url", "", "要載入的網址（開新分頁）")
	attach := flag.String("attach", "", "改為附掛到網址含此字串的既有分頁，不導航")
	settle := flag.Duration("wait", 10*time.Second, "導航後等待渲染的時間")
	outDir := flag.String("out", "dumps", "輸出目錄")
	flag.Parse()

	if *target == "" && *attach == "" {
		log.Fatal("需要 -url 或 -attach")
	}

	browser := rod.New().ControlURL(launcher.MustResolveURL(*cdp)).MustConnect()

	var page *rod.Page
	var err error

	if *attach != "" {
		pages, err := browser.Pages()
		if err != nil {
			log.Fatalf("列出分頁失敗: %v", err)
		}
		for _, p := range pages {
			if info, e := p.Info(); e == nil && strings.Contains(info.URL, *attach) {
				page = p
				log.Printf("附掛到既有分頁: %s", info.URL)
				break
			}
		}
		if page == nil {
			log.Fatalf("找不到網址含 %q 的分頁", *attach)
		}
	} else {
		page, err = browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if err != nil {
			log.Fatalf("開分頁失敗: %v", err)
		}
		defer page.Close()

		log.Printf("導航至 %s", *target)
		if _, err := (proto.PageNavigate{URL: *target}).Call(page); err != nil {
			log.Fatalf("導航失敗: %v", err)
		}
		time.Sleep(*settle)
	}

	info, err := page.Info()
	if err != nil {
		log.Fatalf("取分頁資訊失敗: %v", err)
	}
	log.Printf("最終網址: %s", info.URL)
	log.Printf("標題: %s", info.Title)

	if strings.Contains(info.URL, "/login") || strings.Contains(info.URL, "/checkpoint") {
		log.Printf("⚠ 被導向登入／checkpoint，登入態可能失效")
	}

	// 背景分頁 Chrome 不會渲染，FB 的 feed 會永遠停在骨架狀態。
	if err := (proto.PageBringToFront{}).Call(page); err != nil {
		log.Printf("bringToFront 失敗（繼續）: %v", err)
	}

	html := ""
	deadline := time.Now().Add(*settle)
	for {
		html, err = outerHTML(page)
		if err != nil {
			log.Fatalf("取 DOM 失敗: %v", err)
		}
		// role="article" 是載入骨架，不是貼文。真實單元由 data-virtualized 標記。
		if !strings.Contains(html, `data-visualcompletion="loading-state"`) &&
			strings.Contains(html, "data-virtualized") {
			break
		}
		if time.Now().After(deadline) {
			log.Printf("等待逾時，feed 可能未完全載入")
			break
		}
		time.Sleep(time.Second)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	stamp := time.Now().Format("20060102-150405")
	name := filepath.Join(*outDir, fmt.Sprintf("dump-%s.html", stamp))
	if err := os.WriteFile(name, []byte(html), 0o644); err != nil {
		log.Fatal(err)
	}

	// 截圖：判斷 FB 改版或載入失敗時，畫面比 DOM 直觀
	if shot, err := (proto.PageCaptureScreenshot{Format: "png"}).Call(page); err == nil {
		shotName := filepath.Join(*outDir, fmt.Sprintf("shot-%s.png", stamp))
		if err := os.WriteFile(shotName, shot.Data, 0o644); err == nil {
			fmt.Printf("截圖: %s\n", shotName)
		}
	} else {
		log.Printf("截圖失敗: %v", err)
	}

	ids := map[string]bool{}
	for _, m := range rePost.FindAllStringSubmatch(html, -1) {
		ids[m[1]] = true
	}

	fmt.Printf("\n存檔: %s (%d bytes)\n", name, len(html))
	fmt.Printf("role=\"article\" 數量: %d\n", len(reArticle.FindAllString(html, -1)))
	fmt.Printf("貼文／商品 ID 數量: %d\n", len(ids))
	fmt.Printf("價格樣式命中: %d\n", len(rePrice.FindAllString(html, -1)))
}
