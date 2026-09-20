// surfaceprobe 比較同一個社團的不同頁面，誰先讓新 listing 現身。
//
// 問題不是「離發文多久」—— 那段時間對所有人都不可見，沒有人贏得了。
// 問題是「哪個頁面最早看得到」，因為那決定我們跟其他買家的實際差距。
//
// 只記錄 ID 首次出現的時間，不解析內容、不寫資料庫。
package main

import (
	"flag"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"

	"fbwatch/internal/browser"
)

var reListing = regexp.MustCompile(`/commerce/listing/(\d+)`)

type sighting struct {
	surface string
	at      time.Time
}

func main() {
	cdp := flag.String("cdp", "http://127.0.0.1:9222", "CDP 端點")
	gid := flag.String("group", "", "社團 ID")
	every := flag.Duration("every", 90*time.Second, "每輪間隔")
	dur := flag.Duration("for", 2*time.Hour, "總執行時間")
	flag.Parse()

	if *gid == "" {
		log.Fatal("需要 -group")
	}

	surfaces := []struct{ name, url string }{
		{"時間序動態", fmt.Sprintf("https://www.facebook.com/groups/%s/?sorting_setting=CHRONOLOGICAL", *gid)},
		{"商品分頁", fmt.Sprintf("https://www.facebook.com/groups/%s/", *gid)},
	}

	b := rod.New().ControlURL(launcher.MustResolveURL(*cdp)).MustConnect()

	// id → 各頁面的首次出現時間
	first := map[string]map[string]time.Time{}
	deadline := time.Now().Add(*dur)

	log.Printf("比較 %d 個頁面，每 %s 一輪，共 %s", len(surfaces), *every, *dur)

	for round := 1; time.Now().Before(deadline); round++ {
		for _, s := range surfaces {
			ids, err := listingIDs(b, s.url)
			if err != nil {
				log.Printf("[%s] 失敗: %v", s.name, err)
				continue
			}
			now := time.Now()
			for id := range ids {
				if first[id] == nil {
					first[id] = map[string]time.Time{}
				}
				if _, seen := first[id][s.name]; !seen {
					first[id][s.name] = now
					log.Printf("[%s] 首見 %s", s.name, id)
				}
			}
			time.Sleep(5 * time.Second) // 兩個頁面之間留點間隔
		}
		report(first, surfaces[0].name, surfaces[1].name)
		time.Sleep(*every)
	}
}

func listingIDs(b *rod.Browser, url string) (map[string]bool, error) {
	ready := func(doc string) bool { return strings.Contains(doc, "/commerce/listing/") }
	page, doc, err := browser.LoadPage(b, url, 45*time.Second, ready)
	if page != nil {
		defer page.Close()
	}
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, m := range reListing.FindAllStringSubmatch(doc, -1) {
		out[m[1]] = true
	}
	return out, nil
}

// report 只列出兩個頁面都看過的 ID，那些才有比較意義。
func report(first map[string]map[string]time.Time, a, b string) {
	type row struct {
		id    string
		delta time.Duration // 正值代表 a 比較早
	}
	var rows []row
	for id, m := range first {
		ta, oka := m[a]
		tb, okb := m[b]
		if oka && okb {
			rows = append(rows, row{id, tb.Sub(ta)})
		}
	}
	if len(rows) == 0 {
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].delta < rows[j].delta })

	var aWins, bWins, tie int
	for _, r := range rows {
		switch {
		case r.delta > 10*time.Second:
			aWins++
		case r.delta < -10*time.Second:
			bWins++
		default:
			tie++
		}
	}
	fmt.Printf("  比較 %d 則：%s 早 %d 次，%s 早 %d 次，10 秒內同時 %d 次\n",
		len(rows), a, aWins, b, bWins, tie)
}
