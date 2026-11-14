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
	"fbwatch/internal/keyword"
	"fbwatch/internal/myship"
	"fbwatch/internal/notify"
	"fbwatch/internal/parse"
	"fbwatch/internal/schedule"
	"fbwatch/internal/seed"
	"fbwatch/internal/store"
)

func main() {
	cdp := flag.String("cdp", envOr("FBWATCH_CDP", "http://127.0.0.1:9222"), "Chrome CDP 端點")
	dsn := flag.String("dsn", os.Getenv("FBWATCH_DSN"), "Postgres DSN")
	seedFile := flag.String("seed", envOr("FBWATCH_SEED_FILE", "config/seed.yaml"), "要追什麼的設定檔；僅在對應資料表為空時套用")
	wait := flag.Duration("wait", 60*time.Second, "等待 feed 渲染的上限")
	// 預設 false 且刻意不讀 FBWATCH_LOOP：容器內那個環境變數是給 entrypoint 看的，
	// 若旗標也跟著它，手動 `docker compose exec collector /app/collect` 會意外
	// 起第二個迴圈，讓對 FB 的請求速率加倍。誤跑一次無害，誤開迴圈有風險。
	loop := flag.Bool("loop", false, "持續輪詢，間隔隨機")
	flag.Parse()

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
	// 設定檔的內容只在對應資料表為空時套用，細節見 store.ApplySeed。
	// 設定壞掉就停下來：帶著半套設定跑，症狀會是「什麼都沒通知」。
	sf, err := seed.Load(*seedFile) // 錯誤訊息自帶路徑
	if err != nil {
		log.Fatalf("設定檔讀取失敗: %v", err)
	}
	counts, err := st.ApplySeed(ctx, sf)
	if err != nil {
		log.Fatalf("套用 %s 失敗: %v", *seedFile, err)
	}
	if counts != (store.SeedCounts{}) {
		log.Printf("以 %s 初始化：社團 %d、關鍵字 %d、賣場 %d",
			*seedFile, counts.Groups, counts.Keywords, counts.Shops)
	}
	// 什麼都沒有的話講清楚下一步，不要讓人對著空轉的日誌猜。
	if gs, err := st.EnabledGroups(ctx); err == nil && len(gs) == 0 {
		log.Printf("沒有要監控的社團。複製 config/seed.example.yaml 成 %s 並填入社團 ID。", *seedFile)
	}

	// 比價 bot 查貼文用的唯讀帳號。沒設密碼就維持不能登入。
	if pw := os.Getenv("FBWATCH_MCP_PG_PASSWORD"); pw != "" {
		if err := st.EnableMCPLogin(ctx, pw); err != nil {
			log.Printf("設定唯讀帳號失敗（mcp 服務會連不上，監控照常）: %v", err)
		}
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
	// 社團貼文可以各自分流（groups.webhook_env），告警與賣貨便一律走預設頻道
	router := notify.NewRouter(dc, os.Getenv)

	// 比價 bot 查留言的內網端點。只在輪詢模式開：手動跑一次的行程不該搶這個埠，
	// 也不該在結束時讓端點跟著消失。
	if *loop {
		go serveComments(envOr("FBWATCH_COMMENTS_ADDR", ":8090"), newCommentServer(st, b, os.Getenv("FBWATCH_MCP_TOKEN")))
	}

	sched := schedule.FromEnv(os.Getenv)
	log.Printf("輪詢節奏：日間 %s–%s，夜間 %02d–%02d 點 %s–%s；每輪補抓上限 %d",
		sched.DayMin, sched.DayMax, sched.NightStart, sched.NightEnd,
		sched.NightMin, sched.NightMax, bodiesPerCycle())

	br := breaker.New(maxConsecutiveFailures)
	var silentAlerted bool
	shopFails := newShopFailures()

	// 看門狗跑在自己的 goroutine 上，不能放進下面這個迴圈 ——
	// 它要偵測的就是這個迴圈卡住，一起卡死就什麼都抓不到。
	wd := newWatchdog()
	go wd.watch(ctx, dc, os.Exit)

	for {
		wd.beat("輪詢開始")
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
			wd.beat("社團 " + gid)
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

		wd.beat("賣貨便")
		if err := collectShops(ctx, st, dc, shopFails, wd); err != nil {
			log.Printf("賣貨便抓取失敗: %v", err)
		}
		wd.beat("補抓內文")
		if err := fetchBodies(ctx, st, b, bs, router, wd); err != nil {
			log.Printf("補抓內文失敗: %v", err)
		}
		wd.beat("送出通知")
		if err := sendPending(ctx, st, router); err != nil {
			log.Printf("通知失敗: %v", err)
		}
		wd.beat("靜默檢查")
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
		sleepBeating(ctx, wd, d)
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
	halted.Store(true)
	log.Printf("熔斷：%s —— 停止輪詢，等待人工處理", reason)
	if dc.Enabled() {
		if err := dc.Send(ctx, "⚠️ fbwatch 已熔斷並停止輪詢\n原因："+reason+
			"\n請用 noVNC 重新登入後重啟 collector"); err != nil {
			log.Printf("熔斷告警送出失敗: %v", err)
		}
	}
	select {} // 永久阻塞，不結束行程
}

// alert 把告警同時寫進日誌與 Discord。
//
// 兩邊都要：日誌是事後查的，Discord 是當下會看到的。
// 只寫日誌等於沒有告警 —— 一個賣場連續失敗十天沒人發現就是這樣來的。
func alert(ctx context.Context, dc *notify.Discord, msg string) {
	log.Print(msg)
	if !dc.Enabled() {
		return
	}
	if err := dc.Send(ctx, msg); err != nil {
		log.Printf("告警送出失敗: %v", err)
	}
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
func fetchBodies(ctx context.Context, st *store.Store, b *rod.Browser, bs *blob.Store, router *notify.Router, wd *watchdog) error {
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
		// 逐則報到：這是單步最久的環節，整批算一次會讓看門狗的上限形同虛設
		wd.beat("補抓內文 " + p.ID)

		// 與輪詢請求之間留隨機間隔，不要連續打
		time.Sleep(time.Duration(5+rand.Intn(15)) * time.Second)

		// 先記次數再抓：中途崩潰不該讓這則永遠重試
		if err := st.RecordBodyAttempt(ctx, p.ID); err != nil {
			return err
		}

		// 60 秒而非 30：換帳號後 profile 的 HTTP 快取是空的，
		// 所有資源都要重新下載，實測 30 秒不夠。快取暖起來後會快很多。
		browserMu.Lock()
		page, doc, err := browser.LoadPage(b, p.Permalink, 60*time.Second, ready)
		browser.ClosePage(page)
		browserMu.Unlock()
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

		// 抓完一則就立刻送，不要等整個佇列跑完。
		//
		// 佇列是新的排前面，所以新貼文的內文第一個就到手 —— 但先前要等
		// 後面最多 5 則各自的隨機間隔與載入，實測讓 notice_lag 拖到 1:44。
		if err := sendPending(ctx, st, router); err != nil {
			log.Printf("通知失敗: %v", err)
		}
	}
	return nil
}

// 同一個賣場連續失敗幾次才告警。
//
// 單次失敗很常見（連線抖動、對方重啟），每次都叫會變成雜訊而沒人看。
// 但一直失敗下去也不該安靜：2026-10-27 發現一個賣場連續失敗了十天，
// 每分鐘在日誌裡留一行，沒有任何人知道。
//
// 三個門檻，因為三類錯誤的處置都不一樣：
//
//   - 站台沒反應會自己好。10 分鐘的維護不值得叫人起來，30 分鐘才值得。
//   - 版型變了要改程式，一分鐘都不該等。
//   - 抓取不完整的方向跟前兩者相反 —— 其他失敗是「沒拿到資料」，重試就好；
//     截斷是「拿到了但不敢採信，主動丟掉」，保護機制在這期間把所有商品變動
//     （包含真實的完售與下架）全部擋在外面。連續擋三輪代表要嘛抓取持續不完整，
//     要嘛賣家真的清了四成以上的貨，而後者會讓保護機制永遠擋著，自己不會好。
const (
	shopAlertAfter      = 5  // 版型類：抓到頁面但解析不出商品、非 5xx 的 HTTP 錯誤
	shopAlertAfterSite  = 15 // 站台類：連不上、超時、5xx
	shopAlertAfterTrunc = 3  // 截斷類：商品數掉四成以上
)

// shopFailures 記住每個賣場連續失敗幾次，以及告警過了沒。
// 成功一次就歸零，所以恢復之後再壞還會再叫一次。
type shopFailures struct {
	n       map[string]int
	alerted map[string]bool
}

func newShopFailures() *shopFailures {
	return &shopFailures{n: map[string]int{}, alerted: map[string]bool{}}
}

func (s *shopFailures) ok(id string) {
	delete(s.n, id)
	delete(s.alerted, id)
}

// fail 累計一次失敗，回傳這次要不要告警（只在跨過門檻那次回 true）。
//
// after 由當下這次的錯誤類型決定，不是固定值。計數本身共用：
// 連續失敗就是連續失敗，中途錯誤類型換了也不該歸零。
func (s *shopFailures) fail(id string, after int) bool {
	s.n[id]++
	if s.n[id] >= after && !s.alerted[id] {
		s.alerted[id] = true
		return true
	}
	return false
}

// shopAlert 依錯誤類型決定告警門檻與標題。
//
// 標題必須指對方向：站台沒反應要你等，版型變了要我改程式。
// 兩者都寫成「可能是版型變了」的話，每則告警都得讀第二行才知道該不該理，
// 久了就不會讀了。
func shopAlert(err error) (after int, headline string) {
	switch {
	case errors.Is(err, myship.ErrSiteUnavailable):
		return shopAlertAfterSite, "賣貨便站台沒反應，通常會自己好"
	case errors.Is(err, store.ErrIncompleteFetch):
		return shopAlertAfterTrunc, "連續判定抓取不完整，商品變動已被擋住 —— 若賣家真的清了貨要手動確認"
	}
	return shopAlertAfter, "抓到頁面但解析不出商品，可能是版型變了"
}

// collectShops 抓取 7-11 賣貨便的賣場並通知變動。
//
// 與 FB 那條路完全獨立：純 HTTP、不碰瀏覽器、不碰登入態，
// 所以它失敗不該影響 FB 的輪詢，也不進熔斷計數。
//
// 每個賣場不論成功或失敗都寫一筆 poll_runs。2026-11-02 想回答
// 「午夜是不是每天都掛」時才發現賣貨便這條路從來沒留紀錄，
// 只有 FB 社團有 —— 沒有紀錄就只能看容器還沒輪替掉的那段日誌。
func collectShops(ctx context.Context, st *store.Store, dc *notify.Discord, fails *shopFailures, wd *watchdog) error {
	shops, err := st.EnabledShops(ctx)
	if err != nil || len(shops) == 0 {
		return err
	}
	f := myship.NewFetcher()
	now := time.Now()

	for _, sh := range shops {
		// 逐個賣場報到：賣場數量會成長，整批算一次的話總耗時遲早超過看門狗上限
		wd.beat("賣貨便 " + sh.ID)

		start := time.Now()
		items, err := f.Fetch(ctx, sh.ID)
		if errors.Is(err, myship.ErrShopOffline) {
			// 賣家把賣場關了。這不是會自己好的錯誤，重試只是浪費 ——
			// 講一次、停止追蹤，要回來再手動打開。
			log.Printf("[賣貨便 %s] %s，停止追蹤", sh.ID, err)
			recordShopPoll(ctx, st, sh.ID, start, 0, 0, err)
			if e := st.DisableShop(ctx, sh.ID, "賣家下架，"+now.Format("2006-01-02")+" 自動停止追蹤"); e != nil {
				log.Printf("[賣貨便 %s] 停用失敗: %v", sh.ID, e)
			}
			alert(ctx, dc, fmt.Sprintf("🏪 **%s** 的賣場已被賣家下架，已停止追蹤。\n賣家重新上架後要手動把 `enabled` 打開。", sh.Name))
			fails.ok(sh.ID)
			continue
		}
		if err != nil {
			log.Printf("[賣貨便 %s] 失敗: %v", sh.ID, err)
			recordShopPoll(ctx, st, sh.ID, start, 0, 0, err)
			after, headline := shopAlert(err)
			if fails.fail(sh.ID, after) {
				alert(ctx, dc, fmt.Sprintf("⚠️ **%s** 連續 %d 次抓取失敗 —— %s：\n`%v`",
					sh.Name, after, headline, err))
			}
			continue
		}
		n, seeded, err := st.SyncShopItems(ctx, sh.ID, items, now)
		if err != nil {
			// 不要 return —— 一個賣場寫入失敗不該讓當輪剩下的賣場整個跳過。
			// 抓取成功但寫入失敗仍然是「這輪沒拿到這個賣場的變動」，
			// 所以照樣計入連續失敗，否則截斷保護會一直擋著而沒有人知道。
			log.Printf("[賣貨便 %s] 同步失敗: %v", sh.ID, err)
			recordShopPoll(ctx, st, sh.ID, start, len(items), 0, err)
			after, headline := shopAlert(err)
			if fails.fail(sh.ID, after) {
				alert(ctx, dc, fmt.Sprintf("⚠️ **%s** 連續 %d 輪 —— %s：\n`%v`",
					sh.Name, after, headline, err))
			}
			continue
		}
		fails.ok(sh.ID)
		recordShopPoll(ctx, st, sh.ID, start, len(items), n, nil)
		if seeded {
			// 首次只建基準，否則整個賣場會被當成新上架全部通知出去
			if err := st.MarkAllShopEventsNotified(ctx, sh.ID, now); err != nil {
				return err
			}
			log.Printf("[賣貨便 %s] 首次執行，%d 件設為基準不通知", sh.ID, len(items))
			continue
		}
		log.Printf("[賣貨便 %s] %d 件，變動 %d 筆", sh.ID, len(items), n)
	}
	return sendShopEvents(ctx, st, dc)
}

// recordShopPoll 把一次賣場輪詢寫進 poll_runs，沿用 FB 那張表。
//
// group_id 放賣場編號（GM 開頭，欄位是純 TEXT 沒有外鍵），parsed 是抓到幾件，
// new_items 借來放這輪的變動筆數 —— 賣場沒有「新貼文」，變動數是最接近的對應。
//
// 寫失敗只記日誌不回傳錯誤：這是觀測資料，不該因為它讓整輪輪詢失敗。
func recordShopPoll(ctx context.Context, st *store.Store, shopID string, start time.Time, parsed, changed int, runErr error) {
	if e := st.RecordPoll(ctx, shopID, start, parsed, changed, time.Since(start), runErr); e != nil {
		log.Printf("[賣貨便 %s] 輪詢紀錄寫入失敗: %v", shopID, e)
	}
}

func sendShopEvents(ctx context.Context, st *store.Store, dc *notify.Discord) error {
	if !dc.Enabled() {
		return nil
	}
	events, err := st.PendingShopEvents(ctx, 20)
	if err != nil || len(events) == 0 {
		return err
	}
	rules, err := st.EnabledKeywords(ctx)
	if err != nil {
		return err
	}
	names, err := st.EnabledShops(ctx)
	if err != nil {
		return err
	}
	shopName := map[string]string{}
	for _, s := range names {
		shopName[s.ID] = s.Name
	}

	var sent []int64
	for _, e := range events {
		ev := notify.ShopEvent{
			Kind: e.Kind, ShopName: shopName[e.ShopID], ItemName: e.Name,
			OldPrice: e.OldPrice, NewPrice: e.NewPrice,
			OldQty: e.OldQty, NewQty: e.NewQty,
			ShopURL:  myship.ItemURL(e.ShopID, e.ItemID),
			ImageURL: myship.ImageURL(e.ShopID, e.Image),
			Used:     myship.ConditionLabel(e.Status) == "二手",
		}
		msg, summon := notify.ShopEventMessage(ev), ""
		if summonID != "" && ev.Summonable() && keyword.Summons(rules, e.Name) {
			summon = summonID
			msg = notify.WithMention(msg, summonID)
		}
		if err := dc.SendEmbedMentioning(ctx, msg, notify.ShopEventEmbed(ev), summon); err != nil {
			log.Printf("賣貨便通知送出失敗: %v", err)
			break
		}
		sent = append(sent, e.ID)
		time.Sleep(time.Second) // Discord webhook 有速率限制
	}
	if len(sent) == 0 {
		return nil
	}
	log.Printf("賣貨便已通知 %d 筆", len(sent))
	return st.MarkShopEventsNotified(ctx, sent, time.Now())
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

// groupRouting 是每輪從 groups 表讀出來的兩件事：
// 哪些管道壞了（要整組跳過），以及各社團的主題過濾器。
type groupRouting struct {
	broken  []string
	filters map[string]keyword.Filter
}

// readRouting 讀一次 groups 表就把兩件事都算好。
//
// 壞掉的管道要挑出來讓佇列整個跳過：設錯的 webhook 不該讓貼文被丟掉
// （下一輪還會再試），但也不該讓它卡住別人。
func readRouting(ctx context.Context, st *store.Store, r *notify.Router) (groupRouting, error) {
	gs, err := st.EnabledGroups(ctx)
	if err != nil {
		return groupRouting{}, err
	}
	out := groupRouting{filters: map[string]keyword.Filter{}}
	seen := map[string]bool{}
	for _, g := range gs {
		out.filters[g.ID] = keyword.NewFilter(g.RequirePatterns)
		if g.WebhookEnv == "" || seen[g.WebhookEnv] {
			continue
		}
		seen[g.WebhookEnv] = true
		if _, err := r.For(g.WebhookEnv); err != nil {
			log.Printf("[%s] 通知管道不可用，這輪跳過該社團: %v", g.ID, err)
			out.broken = append(out.broken, g.WebhookEnv)
		}
	}
	return out, nil
}

// allow 判斷這則要不要通知，以及現在就能不能定案。
//
// 命中就馬上送，不等補抓 —— 延遲是這個系統最在意的東西。
// 沒命中則要看內文是不是最終版：feed 上的截斷內文可能根本還沒講到關鍵字，
// 這時候判死會漏掉真正想要的貼文。所以沒命中又還沒定案的先留著，
// 下一輪補到完整內文再重判。重試上限 3 次，不會無限期卡著。
func allow(f keyword.Filter, text string, bodyFinal bool) (send, decided bool) {
	if f.Allows(text) {
		return true, true
	}
	return false, bodyFinal
}

// sendPending 送出尚未通知的 listing：完整內文加連結，內容一字不改。
//
// 超過 Discord 單則上限的目錄型貼文會切成多則，不截斷 ——
// 被截掉的往往正是後半段的品項與價格。
//
// 每則依所屬社團挑聊天室（groups.webhook_env）。挑不到的時候整則跳過
// 而不是退回預設頻道：分流的理由就是「不要送到那裡」。
func sendPending(ctx context.Context, st *store.Store, r *notify.Router) error {
	if !r.Default().Enabled() {
		return nil
	}
	// 先確認哪些社團的通知管道是通的。送不出去的整個排除在佇列外，
	// 否則一個設錯的社團會把 20 則額度塞滿，其他社團跟著不會通知。
	routing, err := readRouting(ctx, st, r)
	if err != nil {
		return err
	}
	pending, err := st.PendingNotifications(ctx, 20, routing.broken)
	if err != nil {
		return err
	}
	// 每輪重讀，改關鍵字表即時生效，不必重啟
	rules, err := st.EnabledKeywords(ctx)
	if err != nil {
		return err
	}

	// 批次內的記憶體集合。跨社團重複的中位數間隔只有 11 秒，兩則幾乎都落在
	// 同一批裡，那時候前一則的 notified_at 還沒寫進資料庫 —— 只靠查詢會兩則都送。
	// 鍵要帶上頻道：不同頻道的同樣內容是兩回事。
	seen := map[string]string{}

	var sent, filtered []string
	var deduped int
	for _, p := range pending {
		// 關鍵字與主題過濾都要比對標題 ——「售如標題」的貼文，
		// 真正的內容只出現在標題裡。
		matchText := p.Title + "\n" + p.Body

		// 社團設了主題過濾就先篩。沒命中而且內文已經定案 → 這輪標掉不再處理；
		// 沒命中但內文還可能變豐富 → 先留著，下一輪補到完整內文再重判。
		if send, decided := allow(routing.filters[p.GroupID], matchText, p.BodyFinal); !send {
			if decided {
				log.Printf("[%s] 不符主題，略過: %s", p.GroupID, firstLine(matchText))
				filtered = append(filtered, p.ID)
			}
			continue
		}

		// 命中在意的主題就在最上面加一行。內文本身一個字都不動 ——
		// 判斷還是交給人，這只是讓你在通知列上就先知道值不值得點開。
		msg := notify.PostMessage(notify.Post{
			Highlight: keyword.Lines(rules, matchText),
			Title:     p.Title,
			Body:      p.Body,
			Permalink: p.Permalink,
		})
		// 靜音的賣家只佔一行。不是不通知 —— 完全不出聲會產生靜默盲區。
		if p.Muted {
			msg = notify.MutedMessage(p.SellerName, p.Permalink, keyword.Labels(rules, matchText))
		}
		// 命中要比價的主題就叫比價 bot 出來。靜音賣家、徵求文不叫 —— 那不是購買機會，叫了只是燒額度。
		summon := ""
		if summonID != "" && !p.Muted && p.Status != "wanted" && keyword.Summons(rules, matchText) {
			summon = summonID
			msg = notify.WithMention(msg, summonID)
		}

		// 同內容去重。擋掉的是「同賣家、同內容、同頻道、已經送過」的重複。
		if p.ContentKey != "" && p.SellerID != "" {
			if of, ok := seen[p.WebhookEnv+"\x00"+p.ContentKey]; ok {
				log.Printf("[%s] 與 %s 同內容，略過", p.GroupID, of)
				if e := st.MarkDuplicate(ctx, p.ID, of); e != nil {
					log.Printf("標記重複失敗 %s: %v", p.ID, e)
				}
				deduped++
				continue
			}
		}
		switch v, of, e := st.DuplicateOf(ctx, p); {
		case e != nil:
			log.Printf("去重查詢失敗 %s: %v", p.ID, e)
		case v == store.DupSuppress:
			log.Printf("[%s] 與 %s 同內容，略過", p.GroupID, of)
			if e := st.MarkDuplicate(ctx, p.ID, of); e != nil {
				log.Printf("標記重複失敗 %s: %v", p.ID, e)
			}
			deduped++
			continue
		case v == store.DupHold:
			// 有疑似對象但內文還沒定案。feed 文字是截斷的，用它判會誤擋 ——
			// 等完整內文再說，延遲只落在很可能冗餘的這一則身上。
			continue
		}

		dc, err := r.For(p.WebhookEnv)
		if err != nil {
			// readRouting 已經先濾過一輪，走到這裡代表剛好在這之間變了
			log.Printf("[%s] 找不到通知管道，跳過: %v", p.GroupID, err)
			continue
		}

		failed := false
		for i, part := range notify.Split(msg, notify.MaxMessage) {
			mention := ""
			if i == 0 {
				mention = summon // mention 在第一行，只有第一則需要放行
			}
			if err := dc.SendMentioning(ctx, part, mention); err != nil {
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
		if p.ContentKey != "" && p.SellerID != "" {
			seen[p.WebhookEnv+"\x00"+p.ContentKey] = p.ID
		}
	}

	// 送出的與濾掉的都要標掉。濾掉的不標的話會一直留在佇列裡重判，
	// 佔滿 20 則的額度，最後連該通知的也擠不進來。
	done := append(sent, filtered...)
	if len(done) == 0 {
		return nil
	}
	if len(sent) > 0 {
		log.Printf("已通知 %d 則", len(sent))
	}
	if len(filtered) > 0 {
		log.Printf("不符主題略過 %d 則", len(filtered))
	}
	if deduped > 0 {
		log.Printf("同內容略過 %d 則", deduped)
	}
	return st.MarkNotified(ctx, done, time.Now())
}

// itemStore 是 storeItems 需要的那一小塊 Store，拆出來才測得動。
type itemStore interface {
	UpsertSeller(ctx context.Context, id, name string) error
	Upsert(ctx context.Context, groupID string, l parse.Listing, now time.Time) (bool, error)
}

// storeItems 寫入本輪抓到的貼文，逐則容錯。
//
// 一則寫壞不該讓整輪失敗。2026-10-16 就是這樣停機的：一篇貼文裡的
// 賣貨便連結被當成價格、數字撐爆 int4，寫入失敗讓兩個社團的輪詢連續
// 三輪掛掉，熔斷把整台停了半小時。解析永遠會有沒想到的輸入，
// 壞掉的那一則跳過就好。
//
// 但「全部都失敗」是另一回事 —— 資料庫掛了、連線斷了 —— 那仍然要往上報，
// 熔斷才接得到真正該停下來的狀況。
func storeItems(ctx context.Context, st itemStore, gid string, items []parse.Listing, now time.Time) (
	stored int, newIDs []string, failed int, err error) {

	var lastErr error
	for _, it := range items {
		if e := st.UpsertSeller(ctx, it.SellerID, it.SellerName); e != nil {
			log.Printf("[%s] 賣家 %s 寫入失敗，跳過這則: %v", gid, it.SellerID, e)
			failed, lastErr = failed+1, e
			continue
		}
		isNew, e := st.Upsert(ctx, gid, it, now)
		if e != nil {
			log.Printf("[%s] 貼文 %s 寫入失敗，跳過: %v", gid, it.ID, e)
			failed, lastErr = failed+1, e
			continue
		}
		stored++
		if isNew {
			newIDs = append(newIDs, it.ID)
		}
	}
	if failed > 0 && stored == 0 {
		return 0, nil, failed, fmt.Errorf("本輪 %d 則全部寫入失敗: %w", failed, lastErr)
	}
	return stored, newIDs, failed, nil
}

func collectGroup(ctx context.Context, st *store.Store, b *rod.Browser, gid string, wait time.Duration) error {
	start := time.Now()
	seeded, err := st.GroupSeeded(ctx, gid)
	if err != nil {
		return err
	}

	browserMu.Lock()
	items, err := fetch(b, gid, wait)
	browserMu.Unlock()
	if err != nil {
		_ = st.RecordPoll(ctx, gid, start, 0, 0, time.Since(start), err)
		return err
	}

	now := time.Now()
	stored, newIDs, failed, err := storeItems(ctx, st, gid, items, now)
	if err != nil {
		_ = st.RecordPoll(ctx, gid, start, len(items), 0, time.Since(start), err)
		return err
	}

	// 首次接觸這個社團：全部標成已通知，否則會對整頁既有貼文發推播
	if !seeded {
		if err := st.MarkNotified(ctx, newIDs, now); err != nil {
			return err
		}
		log.Printf("[%s] 首次執行，%d 則設為基準不通知", gid, len(newIDs))
	}

	if err := st.RecordPoll(ctx, gid, start, stored, len(newIDs), time.Since(start), nil); err != nil {
		return err
	}
	skipped := ""
	if failed > 0 {
		skipped = fmt.Sprintf("，跳過 %d 則", failed)
	}
	log.Printf("[%s] 解析 %d 則，新增 %d 則%s (%s)",
		gid, stored, len(newIDs), skipped, time.Since(start).Round(time.Millisecond))
	for _, it := range items {
		fmt.Printf("    %-18s %-8s price=%-7d %s\n", it.ID, it.Status, it.Price, firstLine(it.Text))
	}
	return nil
}

func fetch(b *rod.Browser, gid string, wait time.Duration) ([]parse.Listing, error) {
	// 不再等時間戳記的隱藏節點出現 —— 那當初是為了從 feed 推算貼文時間，
	// 每個社團要多花 20 秒。現在每則都會抓詳情頁拿精確的 creation_time
	// 覆蓋掉推算值，為它多等只是拉長相位差。
	//
	// 但仍要等到至少兩則：只等第一則時實測每輪固定只解析到 1 則（原本平均 1.52），
	// 而品牌帳號會在同一分鐘連發數則，一輪只看一則就會漏。
	// 上限 4 秒：能湊齊的社團兩三秒內就好，湊不齊的是版面渲染限制、等再久也沒用。
	ready := func(doc string) bool {
		root, err := html.Parse(strings.NewReader(doc))
		return err == nil && len(parse.Articles(root)) >= 2
	}
	page, doc, err := browser.Load(b, browser.GroupURL(gid), wait, 4*time.Second, ready)
	defer browser.ClosePage(page)
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

// firstLine 取第一行有內容的文字，給日誌用。
//
// 跳過空行而不是只取第一行：呼叫端常常傳 標題+"\n"+內文，
// 沒有標題時第一行是空的，只取第一行就會印出一片空白看不出是哪一則。
func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			if r := []rune(ln); len(r) > 40 {
				return string(r[:40]) + "…"
			}
			return ln
		}
	}
	return "(無內文)"
}

// summonID 是比價 bot的 Discord 使用者 ID。命中要比價的主題時，
// 通知裡會 tag 它，讓它自動比價。沒設就不叫。
var summonID = os.Getenv("FBWATCH_SUMMON_USER_ID")

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
