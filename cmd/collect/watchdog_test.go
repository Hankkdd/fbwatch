package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func testWatchdog(limit, tick time.Duration) *watchdog {
	return &watchdog{last: time.Now(), note: "啟動", limit: limit, tick: tick}
}

// 有進展就不該開槍。正常運作下看門狗必須完全安靜，
// 否則它自己就變成故障來源。
func TestWatchdogStaysQuietWhileProgressing(t *testing.T) {
	w := testWatchdog(200*time.Millisecond, 10*time.Millisecond)
	var exits int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.watch(ctx, nil, func(int) { atomic.AddInt32(&exits, 1) })

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.beat("還在跑")
		time.Sleep(20 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&exits); n != 0 {
		t.Fatalf("有進展時不該結束進程，實得 %d 次", n)
	}
}

// 停止回報就要結束進程。這是 2026-11-09 那次停機唯一能救的機制 ——
// 進程活著、Chrome 活著、CDP 有回應，沒有任何既有機制看得出異常。
func TestWatchdogExitsWhenStalled(t *testing.T) {
	w := testWatchdog(50*time.Millisecond, 10*time.Millisecond)
	exited := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.watch(ctx, nil, func(code int) { exited <- code })

	select {
	case code := <-exited:
		if code == 0 {
			t.Error("要用非零離開碼，否則看起來像正常收工")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("停滯超過上限卻沒有結束進程")
	}
}

// 只開一次槍。exit 之後迴圈要收掉，不要連開。
func TestWatchdogExitsOnlyOnce(t *testing.T) {
	w := testWatchdog(20*time.Millisecond, 5*time.Millisecond)
	var exits int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.watch(ctx, nil, func(int) { atomic.AddInt32(&exits, 1) })

	time.Sleep(300 * time.Millisecond)
	if n := atomic.LoadInt32(&exits); n != 1 {
		t.Errorf("應該只結束一次，實得 %d 次", n)
	}
}

// ctx 取消要能收工，不要留下跑不完的 goroutine
func TestWatchdogStopsOnContextCancel(t *testing.T) {
	w := testWatchdog(10*time.Millisecond, 5*time.Millisecond)
	var exits int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.watch(ctx, nil, func(int) { atomic.AddInt32(&exits, 1) }); close(done) }()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消後看門狗沒有收工")
	}
}

// beat 記下的位置要出現在告警裡，不然只知道卡住、不知道卡在哪
func TestWatchdogRemembersWhere(t *testing.T) {
	w := testWatchdog(time.Minute, time.Second)
	w.beat("社團 100000000000004")
	d, where := w.stalled()
	if where != "社團 100000000000004" {
		t.Errorf("實得 %q", where)
	}
	if d > time.Second {
		t.Errorf("剛報到過，停滯時間應該接近 0，實得 %s", d)
	}
}

// 等待下一輪比看門狗上限還長時不該被誤殺。
//
// 這是 2026-11-10 02:05 那次誤判的回歸測試：夜間間隔 5–10 分鐘，
// 上限 5 分鐘，等待期間沒報到就會每隔五分鐘自己重啟一次。
func TestSleepBeatingSurvivesWaitLongerThanLimit(t *testing.T) {
	w := testWatchdog(100*time.Millisecond, 10*time.Millisecond)
	var exits int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.watch(ctx, nil, func(int) { atomic.AddInt32(&exits, 1) })

	sleepBeating(ctx, w, 300*time.Millisecond)

	if n := atomic.LoadInt32(&exits); n != 0 {
		t.Fatalf("等待下一輪不是停滯，不該結束進程，實得 %d 次", n)
	}
}

// 等待結束就要停止報到 —— 不然計時器到了之後它還在幫忙掩護，
// 真的卡在下一輪的工作上也看不出來。
func TestSleepBeatingStopsBeatingWhenWaitEnds(t *testing.T) {
	w := testWatchdog(time.Minute, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sleepBeating(ctx, w, 30*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	if d, _ := w.stalled(); d < 80*time.Millisecond {
		t.Fatalf("等待結束後仍在報到：停滯只算到 %s", d)
	}
}

// 熔斷之後看門狗不能再開槍。
//
// 2026-11-24 的事故：帳號被 checkpoint，熔斷停了下來，但 halt() 是永久阻塞，
// 不再向看門狗報到，於是看門狗每 5 分鐘判定「卡死」砍掉進程，容器重啟又去打
// 一次 FB。6.5 小時內撞了 76 次 checkpoint —— 兩個保護機制互相抵銷，
// 做出來的事正好是熔斷要避免的。
func TestWatchdogStandsDownAfterHalt(t *testing.T) {
	halted.Store(true)
	defer halted.Store(false)

	w := testWatchdog(50*time.Millisecond, 10*time.Millisecond)
	var exits int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.watch(ctx, nil, func(int) { atomic.AddInt32(&exits, 1) })

	// 完全不報到，停滯時間遠超上限
	time.Sleep(300 * time.Millisecond)

	if n := atomic.LoadInt32(&exits); n != 0 {
		t.Fatalf("熔斷後不該再結束進程，實得 %d 次", n)
	}
}
