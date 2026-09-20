// collect 抓取社團時間序動態、解析、寫入 Postgres，並把新 listing 的連結送到 Discord。
//
// 預設執行一次就結束。加 -loop（或 FBWATCH_LOOP=1）持續輪詢，
// 間隔隨機且夜間拉長 —— 固定間隔本身就是機器人簽章。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"golang.org/x/net/html"

	"fbwatch/internal/blob"
	"fbwatch/internal/breaker"
	"fbwatch/internal/browser"
	"fbwatch/internal/images"
	"fbwatch/internal/notify"
	"fbwatch/internal/parse"
	"fbwatch/internal/schedule"
	"fbwatch/internal/store"
)

func main() {
	cdp := flag.String("cdp", envOr("FBWATCH_CDP", "http://127.0.0.1:9222"), "Chrome CDP 端點")
	dsn := flag.String("dsn", os.Getenv("FBWATCH_DSN"), "Postgres DSN")
	groups := flag.String("groups", os.Getenv("FBWATCH_GROUPS"), "社團 ID 逗號分隔；僅在 groups 表為空時用來初始化")
	wait := flag.Duration("wait", 60*time.Second, "等待 feed 渲染的上限")
	// 預設 false 且刻意不讀 FBWATCH_LOOP：容器內那個環境變數是給 entrypoint 看的，
	// 若旗標也跟著它，手動 `docker compose exec collector /app/collect` 會意外
	// 起第二個迴圈，讓對 FB 的請求速率加倍。誤跑一次無害，誤開迴圈有風險。
	loop := flag.Bool("loop", false, "持續輪詢，間隔隨機")
	flag.Parse()

	// 社團清單以 groups 表為準，所以這裡不再要求 -groups
	if *dsn == "" {
		log.Fatal("需要 -dsn（或 FBWATCH_DSN）")
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
	// 圖片儲存是加值功能：連不上就不存圖，不該讓整個監控停擺
	var bs *blob.Store
	if ep := os.Getenv("MINIO_ENDPOINT"); ep != "" {
		bs, err = blob.Open(ctx, ep, os.Getenv("MINIO_USER"),
			os.Getenv("MINIO_PASSWORD"), envOr("MINIO_BUCKET", "listing-images"))
		if err != nil {
			log.Printf("MinIO 連線失敗，本次不保存圖片: %v", err)
			bs = nil
		}
	}

	dc := notify.NewDiscord(os.Getenv("DISCORD_WEBHOOK_URL"))
	if !dc.Enabled() {
		log.Print("未設 DISCORD_WEBHOOK_URL，不會送出通知")
	}

	var seedIDs []string
	for _, gid := range strings.Split(*groups, ",") {
		if gid = strings.TrimSpace(gid); gid != "" {
			seedIDs = append(seedIDs, gid)
		}
	}
	if n, err := st.SeedGroups(ctx, seedIDs); err != nil {
		log.Fatalf("初始化社團清單失敗: %v", err)
	} else if n > 0 {
		log.Printf("以 FBWATCH_GROUPS 初始化 %d 個社團", n)
	}

	sched := schedule.FromEnv(os.Getenv)
	log.Printf("輪詢節奏：日間 %s–%s，夜間 %s–%s；每輪補抓上限 %d",
		sched.DayMin, sched.DayMax, sched.NightMin, sched.NightMax, bodiesPerCycle())

	br := breaker.New(maxConsecutiveFailures)
	var silentAlerted bool

	for {
		// 每輪重讀，在資料庫改 enabled 即時生效，不必重啟
		gs, err := st.EnabledGroups(ctx)
		if err != nil {
			log.Fatalf("讀取社團清單失敗: %v", err)
		}
		if len(gs) == 0 {
			log.Print("沒有啟用中的社團，這輪跳過")
		}

		for _, g := range gs {
			gid := g.ID
			err := collectGroup(ctx, st, b, gid, *wait)
			switch {
			case err == nil:
				br.Success()
			case errors.Is(err, browser.ErrSessionInvalid):
				log.Printf("[%s] %v", gid, err)
				br.Fatal(err.Error())
			default:
				log.Printf("[%s] 失敗: %v", gid, err)
				br.Fail(err.Error())
			}
			if br.Tripped() {
				break
			}
		}

		if br.Tripped() {
			halt(ctx, dc, br.Reason())
			return
		}

		if err := fetchBodies(ctx, st, b, bs); err != nil {
			log.Printf("補抓內文失敗: %v", err)
		}
		if err := sendPending(ctx, st, dc); err != nil {
			log.Printf("通知失敗: %v", err)
		}
		checkSilence(ctx, st, dc, &silentAlerted)

		if !*loop {
			return
		}

		d := sched.Next(time.Now())
		if br.State() == breaker.Cooling {
			d = coolDown
			log.Printf("連續失敗，冷卻 %s 後重試一次：%s", d, br.Reason())
		}
		log.Printf("下次輪詢 %s 後", d.Round(time.Second))
		time.Sleep(d)
	}
}

const (
	maxConsecutiveFailures = 3
	coolDown               = 30 * time.Minute
	silenceThreshold       = 6 * time.Hour
)

// halt 在熔斷後停止輪詢，但讓行程活著。
//
// 不能直接結束：容器設了 restart，結束會被重啟，然後再試一次、再熔斷，
// 變成「每次重啟打 FB 一次」的迴圈 —— 正是熔斷要避免的事。
// 留著行程也讓 noVNC 仍可連入重新登入。
func halt(ctx context.Context, dc *notify.Discord, reason string) {
	log.Printf("熔斷：%s —— 停止輪詢，等待人工處理", reason)
	if dc.Enabled() {
		if err := dc.Send(ctx, "⚠️ fbwatch 已熔斷並停止輪詢\n原因："+reason+
			"\n請用 noVNC 重新登入後重啟 collector"); err != nil {
			log.Printf("熔斷告警送出失敗: %v", err)
		}
	}
	select {} // 永久阻塞，不結束行程
}

// checkSilence 偵測靜默失效：解析器被 FB 改版打壞時，系統看起來一切正常
// （抓得到頁面、沒有錯誤）但永遠不會有新項目。這比明顯崩潰更危險。
func checkSilence(ctx context.Context, st *store.Store, dc *notify.Discord, alerted *bool) {
	last, ok, err := st.LastNewItem(ctx)
	if err != nil || !ok {
		return
	}
	quiet := time.Since(last)
	if quiet < silenceThreshold {
		*alerted = false
		return
	}
	if *alerted {
		return
	}
	*alerted = true
	msg := fmt.Sprintf("⚠️ fbwatch 已 %s 沒有新項目，解析器可能已被 FB 改版打壞",
		quiet.Round(time.Minute))
	log.Print(msg)
	if dc.Enabled() {
		if err := dc.Send(ctx, msg); err != nil {
			log.Printf("靜默告警送出失敗: %v", err)
		}
	}
}

// bodiesPerCycle 限制每輪補抓的則數。
//
// 這是輪詢之外的額外請求，量要壓住；剩下的下一輪會接著處理。
// 但上限太低時，一次湧入多則新貼文會讓後面的跨輪等待 ——
// 實測看過通知延遲因此拉到 7 分半。
func bodiesPerCycle() int {
	n := 3
	if v := os.Getenv("FBWATCH_BODIES_PER_CYCLE"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 20 {
			n = parsed
		}
	}
	return n
}

// fetchBodies 對內文被截斷的 listing 抓詳情頁，取完整內容。
//
// feed 上的內文是伺服器端截斷的，詳情頁的 DOM 也是 —— 但詳情頁的內嵌 JSON
// 是完整的，所以不需要點「查看更多」，沒有任何互動。
// 同時能取得精確的 creation_time，比 feed 的相對時間推算準得多。
func fetchBodies(ctx context.Context, st *store.Store, b *rod.Browser, bs *blob.Store) error {
	pending, err := st.PendingBodies(ctx, bodiesPerCycle())
	if err != nil || len(pending) == 0 {
		return err
	}

	// 商品頁的內文在 redacted_description，一般貼文頁在 message.text。
	// 兩種頁型都要能過，否則貼文類的永遠等到逾時。
	ready := func(doc string) bool {
		return strings.Contains(doc, "redacted_description") ||
			strings.Contains(doc, `"message":{"text"`)
	}

	for _, p := range pending {
		// 與輪詢請求之間留隨機間隔，不要連續打
		time.Sleep(time.Duration(5+rand.Intn(15)) * time.Second)

		// 先記次數再抓：中途崩潰不該讓這則永遠重試
		if err := st.RecordBodyAttempt(ctx, p.ID); err != nil {
			return err
		}

		// 60 秒而非 30：換帳號後 profile 的 HTTP 快取是空的，
		// 所有資源都要重新下載，實測 30 秒不夠。快取暖起來後會快很多。
		page, doc, err := browser.LoadPage(b, p.Permalink, 60*time.Second, ready)
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
		n := saveImages(ctx, st, bs, p.ID, detail.Photos)
		log.Printf("補抓 %s 完成（%d 字，%d 圖，%s）", p.ID,
			len([]rune(detail.Description)), n, detail.CreatedAt.Format("01-02 15:04"))
	}
	return nil
}

// saveImages 下載並保存商品照片，回傳成功的張數。
//
// 圖片走 CDN，不需要登入態也不需要瀏覽器，所以對帳號風險幾乎為零。
// 但網址帶簽章會過期 —— 必須在這裡就下載，不能只存網址等之後再說。
//
// 單張失敗不影響其他張，也不影響內文：圖片是加值，不是必要條件。
func saveImages(ctx context.Context, st *store.Store, bs *blob.Store, listingID string, photos []parse.Photo) int {
	if bs == nil || len(photos) == 0 {
		return 0
	}
	f := images.NewFetcher()
	saved := 0

	for i, p := range photos {
		// 下載前先用 FB 的 photo_id 去重，省下重複下載
		if sum, ok, err := st.SHA256ForPhoto(ctx, p.ID); err == nil && ok {
			if err := st.LinkImage(ctx, listingID, i, p.ID, sum, p.URL, p.Caption); err == nil {
				saved++
			}
			continue
		}

		res, err := f.Fetch(ctx, p.URL)
		if err != nil {
			log.Printf("圖片 %s[%d] 下載失敗: %v", listingID, i, err)
			// 仍記錄一列，保留順序與網址供日後查證
			_ = st.LinkImage(ctx, listingID, i, p.ID, "", p.URL, p.Caption)
			continue
		}
		if err := bs.Put(ctx, res.Key(), res.Bytes, res.ContentType); err != nil {
			log.Printf("圖片 %s[%d] 寫入失敗: %v", listingID, i, err)
			continue
		}
		if err := st.RecordImage(ctx, res.SHA256, res.Key(), len(res.Bytes), res.ContentType); err != nil {
			log.Printf("圖片 %s[%d] 登記失敗: %v", listingID, i, err)
			continue
		}
		if err := st.LinkImage(ctx, listingID, i, p.ID, res.SHA256, p.URL, p.Caption); err != nil {
			log.Printf("圖片 %s[%d] 連結失敗: %v", listingID, i, err)
			continue
		}
		saved++
	}

	if saved > 0 {
		_ = st.MarkImagesFetched(ctx, listingID)
	}
	return saved
}

// sendPending 送出尚未通知的 listing：連結加完整內文，內容一字不改。
//
// 超過 Discord 單則上限的目錄型貼文會切成多則，不截斷 ——
// 被截掉的往往正是後半段的品項與價格。
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
		// 用 <> 包住網址，Discord 就不會展開連結預覽卡片。
		// FB 的預覽卡片又大又沒資訊量，把真正要看的內文擠到畫面外。
		msg := "<" + p.Permalink + ">"
		if p.Body != "" {
			msg += "\n" + p.Body
		}

		failed := false
		for _, part := range notify.Split(msg, notify.MaxMessage) {
			if err := dc.Send(ctx, part); err != nil {
				// 送失敗就不標記，下一輪整則重送
				log.Printf("送出 %s 失敗: %v", p.ID, err)
				failed = true
				break
			}
			time.Sleep(time.Second) // Discord webhook 有速率限制
		}
		if failed {
			break
		}
		sent = append(sent, p.ID)
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
		return nil, fmt.Errorf("%w：被導向 %s", browser.ErrSessionInvalid, url)
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
