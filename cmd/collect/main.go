// collect 抓取社團時間序動態、解析、寫入 Postgres，並把新 listing 的連結送到 Discord。
//
// 預設執行一次就結束。加 -loop（或 FBWATCH_LOOP=1）持續輪詢，
// 間隔隨機且夜間拉長 —— 固定間隔本身就是機器人簽章。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"golang.org/x/net/html"

	"fbwatch/internal/browser"
	"fbwatch/internal/notify"
	"fbwatch/internal/parse"
	"fbwatch/internal/schedule"
	"fbwatch/internal/store"
)

func main() {
	cdp := flag.String("cdp", envOr("FBWATCH_CDP", "http://127.0.0.1:9222"), "Chrome CDP 端點")
	dsn := flag.String("dsn", os.Getenv("FBWATCH_DSN"), "Postgres DSN")
	groups := flag.String("groups", os.Getenv("FBWATCH_GROUPS"), "社團 ID，逗號分隔")
	wait := flag.Duration("wait", 60*time.Second, "等待 feed 渲染的上限")
	// 預設 false 且刻意不讀 FBWATCH_LOOP：容器內那個環境變數是給 entrypoint 看的，
	// 若旗標也跟著它，手動 `docker compose exec collector /app/collect` 會意外
	// 起第二個迴圈，讓對 FB 的請求速率加倍。誤跑一次無害，誤開迴圈有風險。
	loop := flag.Bool("loop", false, "持續輪詢，間隔隨機")
	flag.Parse()

	if *dsn == "" || *groups == "" {
		log.Fatal("需要 -dsn 與 -groups（或對應的環境變數）")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migration 失敗: %v", err)
	}

	b := rod.New().ControlURL(launcher.MustResolveURL(*cdp)).MustConnect()
	dc := notify.NewDiscord(os.Getenv("DISCORD_WEBHOOK_URL"))
	if !dc.Enabled() {
		log.Print("未設 DISCORD_WEBHOOK_URL，不會送出通知")
	}

	var ids []string
	for _, gid := range strings.Split(*groups, ",") {
		if gid = strings.TrimSpace(gid); gid != "" {
			ids = append(ids, gid)
		}
	}

	sched := schedule.Default()
	for {
		for _, gid := range ids {
			if err := collectGroup(ctx, st, b, gid, *wait); err != nil {
				log.Printf("[%s] 失敗: %v", gid, err)
			}
		}
		if err := fetchBodies(ctx, st, b); err != nil {
			log.Printf("補抓內文失敗: %v", err)
		}
		if err := sendPending(ctx, st, dc); err != nil {
			log.Printf("通知失敗: %v", err)
		}
		if !*loop {
			return
		}
		d := sched.Next(time.Now())
		log.Printf("下次輪詢 %s 後", d.Round(time.Second))
		time.Sleep(d)
	}
}

// bodiesPerCycle 限制每輪補抓的則數。
// 這是輪詢之外的額外請求，量要壓住；剩下的下一輪會接著處理。
const bodiesPerCycle = 3

// fetchBodies 對內文被截斷的 listing 抓詳情頁，取完整內容。
//
// feed 上的內文是伺服器端截斷的，詳情頁的 DOM 也是 —— 但詳情頁的內嵌 JSON
// 是完整的，所以不需要點「查看更多」，沒有任何互動。
// 同時能取得精確的 creation_time，比 feed 的相對時間推算準得多。
func fetchBodies(ctx context.Context, st *store.Store, b *rod.Browser) error {
	pending, err := st.PendingBodies(ctx, bodiesPerCycle)
	if err != nil || len(pending) == 0 {
		return err
	}

	ready := func(doc string) bool { return strings.Contains(doc, "redacted_description") }

	for _, p := range pending {
		// 與輪詢請求之間留隨機間隔，不要連續打
		time.Sleep(time.Duration(5+rand.Intn(15)) * time.Second)

		// 先記次數再抓：中途崩潰不該讓這則永遠重試
		if err := st.RecordBodyAttempt(ctx, p.ID); err != nil {
			return err
		}

		page, doc, err := browser.LoadPage(b, p.Permalink, 30*time.Second, ready)
		if page != nil {
			_ = page.Close()
		}
		if err != nil {
			log.Printf("補抓 %s 失敗: %v", p.ID, err)
			continue
		}
		detail, ok := parse.ListingPage(doc)
		if !ok {
			log.Printf("補抓 %s：頁面沒有商品資料，略過", p.ID)
			continue
		}
		if err := st.UpdateBody(ctx, p.ID, detail); err != nil {
			return err
		}
		log.Printf("補抓 %s 完成（%d 字，%s）", p.ID,
			len([]rune(detail.Description)), detail.CreatedAt.Format("01-02 15:04"))
	}
	return nil
}

// sendPending 送出尚未通知的 listing。目前只傳連結。
func sendPending(ctx context.Context, st *store.Store, dc *notify.Discord) error {
	if !dc.Enabled() {
		return nil
	}
	pending, err := st.PendingNotifications(ctx, 20)
	if err != nil {
		return err
	}
	var sent []string
	for _, p := range pending {
		if err := dc.Send(ctx, p.Permalink); err != nil {
			// 送失敗就不要標記，下一輪會重試
			log.Printf("送出 %s 失敗: %v", p.ID, err)
			break
		}
		sent = append(sent, p.ID)
		time.Sleep(time.Second) // Discord webhook 有速率限制
	}
	if len(sent) == 0 {
		return nil
	}
	log.Printf("已通知 %d 則", len(sent))
	return st.MarkNotified(ctx, sent, time.Now())
}

func collectGroup(ctx context.Context, st *store.Store, b *rod.Browser, gid string, wait time.Duration) error {
	start := time.Now()
	seeded, err := st.GroupSeeded(ctx, gid)
	if err != nil {
		return err
	}

	items, err := fetch(b, gid, wait)
	if err != nil {
		_ = st.RecordPoll(ctx, gid, start, 0, 0, time.Since(start), err)
		return err
	}

	now := time.Now()
	var newIDs []string
	for _, it := range items {
		isNew, err := st.Upsert(ctx, gid, it, now)
		if err != nil {
			return err
		}
		if isNew {
			newIDs = append(newIDs, it.ID)
		}
	}

	// 首次接觸這個社團：全部標成已通知，否則會對整頁既有貼文發推播
	if !seeded {
		if err := st.MarkNotified(ctx, newIDs, now); err != nil {
			return err
		}
		log.Printf("[%s] 首次執行，%d 則設為基準不通知", gid, len(newIDs))
	}

	if err := st.RecordPoll(ctx, gid, start, len(items), len(newIDs), time.Since(start), nil); err != nil {
		return err
	}
	log.Printf("[%s] 解析 %d 則，新增 %d 則 (%s)", gid, len(items), len(newIDs), time.Since(start).Round(time.Millisecond))
	for _, it := range items {
		fmt.Printf("    %-18s %-8s price=%-7d %s\n", it.ID, it.Status, it.Price, firstLine(it.Text))
	}
	return nil
}

func fetch(b *rod.Browser, gid string, wait time.Duration) ([]parse.Listing, error) {
	// 時間戳記的隱藏節點晚於貼文容器出現。少了它是靜默失效 ——
	// 貼文抓得到、沒有錯誤，只有時間永遠是空的，延遲量測就永遠算不出來。
	ready := func(doc string) bool {
		root, err := html.Parse(strings.NewReader(doc))
		if err != nil {
			return false
		}
		items := parse.Articles(root)
		if len(items) == 0 {
			return false
		}
		for _, it := range items {
			if !it.PostedAt.IsZero() {
				return true
			}
		}
		return false
	}

	page, doc, err := browser.Load(b, browser.GroupURL(gid), wait, 20*time.Second, ready)
	if page != nil {
		defer page.Close()
	}
	if err != nil {
		return nil, err
	}

	if out, url := browser.LoggedOut(page); out {
		return nil, fmt.Errorf("被導向 %s —— 登入態失效，停止", url)
	}

	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return nil, err
	}
	return parse.Articles(root), nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\n"); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
