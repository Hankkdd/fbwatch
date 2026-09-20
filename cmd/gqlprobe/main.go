// gqlprobe 是一次性的探測工具：攔下社團動態的 GraphQL 回應，
// 回答兩個問題 —— 內文是否完整、一次回幾則。
//
// 刻意不做任何解析或儲存到資料庫。回應裡可能混有 Messenger、通知等
// 與商品無關的資料，只寫到 /tmp 供人工檢查，看完就刪。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"fbwatch/internal/browser"
)

func main() {
	cdp := flag.String("cdp", "http://127.0.0.1:9222", "Chrome CDP 端點")
	gid := flag.String("group", "", "社團 ID")
	wait := flag.Duration("wait", 45*time.Second, "收集時間")
	outDir := flag.String("out", "/tmp/gql", "輸出目錄")
	flag.Parse()

	if *gid == "" {
		log.Fatal("需要 -group")
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		log.Fatal(err)
	}

	b := rod.New().ControlURL(launcher.MustResolveURL(*cdp)).MustConnect()
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		log.Fatal(err)
	}
	defer page.Close()

	if err := (proto.NetworkEnable{}).Call(page); err != nil {
		log.Fatal(err)
	}

	var mu sync.Mutex
	interesting := map[proto.NetworkRequestID]string{}
	bodies := map[proto.NetworkRequestID]string{}

	stop := page.EachEvent(
		func(e *proto.NetworkResponseReceived) {
			if strings.Contains(e.Response.URL, "/api/graphql/") {
				mu.Lock()
				interesting[e.RequestID] = e.Response.URL
				mu.Unlock()
			}
		},
		func(e *proto.NetworkLoadingFinished) {
			mu.Lock()
			_, ok := interesting[e.RequestID]
			mu.Unlock()
			if !ok {
				return
			}
			res, err := (proto.NetworkGetResponseBody{RequestID: e.RequestID}).Call(page)
			if err != nil {
				return
			}
			mu.Lock()
			bodies[e.RequestID] = res.Body
			mu.Unlock()
		},
	)
	go stop()

	log.Printf("導向社團 %s", *gid)
	if _, err := (proto.PageNavigate{URL: browser.GroupURL(*gid)}).Call(page); err != nil {
		log.Fatal(err)
	}
	_ = (proto.PageBringToFront{}).Call(page)
	time.Sleep(*wait)

	mu.Lock()
	defer mu.Unlock()
	log.Printf("攔到 %d 個 GraphQL 回應，取得主體 %d 個", len(interesting), len(bodies))

	report(bodies, *outDir)
}

var (
	reListingID = regexp.MustCompile(`"id":"(\d{10,})"`)
	reCreation  = regexp.MustCompile(`"creation_time":(\d{9,})`)
	reTruncMark = regexp.MustCompile(`查看更多|\\u67e5\\u770b\\u66f4\\u591a`)
)

// report 只輸出聚合統計與欄位名稱，不印回應內容 ——
// 這些主體裡可能有 Messenger 或通知資料。
func report(bodies map[proto.NetworkRequestID]string, outDir string) {
	type stat struct {
		id                             string
		size                           int
		listings, creations, msgFields int
		hasTrunc                       bool
	}
	var stats []stat

	for rid, body := range bodies {
		name := filepath.Join(outDir, fmt.Sprintf("gql-%s.json", rid))
		_ = os.WriteFile(name, []byte(body), 0o600)

		stats = append(stats, stat{
			id:        string(rid),
			size:      len(body),
			listings:  len(uniq(reListingID.FindAllStringSubmatch(body, -1))),
			creations: len(reCreation.FindAllString(body, -1)),
			msgFields: strings.Count(body, "redacted_description") + strings.Count(body, `"message":`),
			hasTrunc:  reTruncMark.MatchString(body),
		})
	}

	fmt.Printf("\n%-22s %10s %9s %10s %10s %8s\n",
		"requestId", "bytes", "id數", "creation數", "內文欄位", "有截斷標記")
	for _, s := range stats {
		fmt.Printf("%-22s %10d %9d %10d %10d %8v\n",
			s.id, s.size, s.listings, s.creations, s.msgFields, s.hasTrunc)
	}
	fmt.Printf("\n原始回應已寫入 %s（含無關資料，檢查完請刪除）\n", outDir)
}

func uniq(ms [][]string) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		out[m[1]] = true
	}
	return out
}
