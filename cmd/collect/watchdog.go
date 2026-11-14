package main

import (
	"context"
	"log"
	"sync"
	"time"

	"fbwatch/internal/notify"
)

// watchdogLimit 是兩次進展之間容許的最大間隔。
//
// 正常情況下進展很密集：每抓完一個社團、每抓完一則內文、每抓完一個賣場
// 都會報到一次，所以間隔上限其實是單一個操作的耗時 —— 最慢的是補抓內文
// （載入上限 60 秒，加上事前 5–20 秒的隨機間隔），約 80 秒。
// 設 5 分鐘是留三倍餘裕，寧可晚點發現也不要在機器忙的時候誤殺。
const watchdogLimit = 5 * time.Minute

// watchdogTick 是檢查頻率。比上限小得多，發現延遲才不會疊加。
const watchdogTick = 30 * time.Second

// watchdog 偵測「卡住」——既不成功也不失敗的那種死法。
//
// 熔斷器數的是失敗，checkSilence 看的是有沒有新項目，兩者都要迴圈還在跑
// 才有機會執行。2026-11-09 主 goroutine 卡在一個不回應的 CDP 呼叫上 89 分鐘：
// 進程活著、Chrome 活著、CDP 端點還會回應，所以 Docker 不重啟、熔斷器數不到
// 任何失敗、而 checkSilence 寫在卡住的那個迴圈裡面，永遠輪不到它執行。
//
// 看門狗必須跑在自己的 goroutine 上。放進迴圈裡就會跟著一起卡死 ——
// 監控卡住的那個東西的看門狗，不能被關在它要監控的東西裡面。
type watchdog struct {
	mu   sync.Mutex
	last time.Time
	note string

	// 上限與檢查頻率做成欄位而不是直接讀常數，測試才不用等五分鐘
	limit time.Duration
	tick  time.Duration
}

func newWatchdog() *watchdog {
	return &watchdog{last: time.Now(), note: "啟動", limit: watchdogLimit, tick: watchdogTick}
}

// beat 回報有進展。where 會出現在卡住時的告警裡，用來指出停在哪一步。
func (w *watchdog) beat(where string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last = time.Now()
	w.note = where
}

// stalled 回報停滯多久、停在哪一步。
func (w *watchdog) stalled() (time.Duration, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return time.Since(w.last), w.note
}

// watch 盯著進展，超過上限就告警並讓進程結束。
//
// 結束而不是試著恢復：卡住的是別人的狀態（Chrome 的某個分頁、某條 CDP 連線），
// 這支程式沒有能力把它清乾淨。entrypoint 的 `wait -n` 會跟著收工，
// compose 的 restart: unless-stopped 會把整個容器連同 Chrome 一起重來。
// 重啟會掉幾分鐘的資料，卡住會掉幾小時。
func (w *watchdog) watch(ctx context.Context, dc *notify.Discord, exit func(int)) {
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d, where := w.stalled()
			if d < w.limit {
				continue
			}
			msg := "🔁 fbwatch 卡住了，重新啟動\n" +
				"最後一次進展：" + where + "（" + d.Truncate(time.Second).String() + "前）\n" +
				"這通常是某個 CDP 呼叫沒有回應。重啟會連 Chrome 一起重來。"
			// alert 是同步的（Discord client 自帶 15 秒上限），
			// 回來的時候訊息已經送完或失敗了，不需要再等。
			alert(ctx, dc, msg)
			log.Print("看門狗：停滯逾時，結束進程讓容器重啟")
			exit(1)
			return
		}
	}
}

// sleepBeating 等待下一輪，期間照樣向看門狗報到。
//
// 等待是預期中的閒置，不是卡住，可是夜間間隔抽到 5–10 分鐘，比看門狗上限還長。
// 直接 time.Sleep 會讓程式被自己的保護砍掉：2026-11-10 02:05 就是這樣誤殺的 ——
// 02:00 那輪抽到 7m13s，睡到第 5 分 14 秒被判定停滯重啟。
//
// 這不是在放寬保護。等待期間唯一在跑的東西是一個計時器，不會有 CDP 呼叫可以卡住；
// 看門狗要抓的是工作中途沒有回應，而工作要等到計時器到了才開始。
func sleepBeating(ctx context.Context, wd *watchdog, d time.Duration) {
	done := time.NewTimer(d)
	defer done.Stop()
	t := time.NewTicker(wd.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done.C:
			return
		case <-t.C:
			wd.beat("等待下一輪")
		}
	}
}
