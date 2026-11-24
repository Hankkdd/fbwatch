package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"fbwatch/internal/parse"
)

// 需要一個可丟棄的 Postgres。未設 FBWATCH_TEST_DSN 就跳過，
// 讓 `go test ./...` 在沒有資料庫的環境下仍然可跑。
//
//	docker compose exec postgres createdb -U fbwatch fbwatch_test
//	FBWATCH_TEST_DSN=postgres://fbwatch:pw@localhost:55432/fbwatch_test?sslmode=disable go test ./internal/store/
//
// 這裡的測試會 DELETE 整張 groups / keywords / shops / listings（見 clearSeedTables），
// 所以**資料庫名稱必須以 _test 結尾**，否則直接 Fatal。
//
// 這道檢查是 2026-11-24 補的：那天把正式庫的 DSN 餵進來跑測試，
// 社團、關鍵字、賣場與兩個月的 listings 全部被清掉。設定是用 pageinspect
// 從 heap 的死亡 tuple 挖回來的；listings 在刪除後幾秒就被 autovacuum
// 回收，救不回來。正式庫跟測試庫只差一個資料庫名稱，
// 靠人每次記得打對，遲早會再發生一次 —— 所以改成機器擋。
const testDBSuffix = "_test"

func testStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("FBWATCH_TEST_DSN")
	if dsn == "" {
		t.Skip("未設 FBWATCH_TEST_DSN，跳過資料庫測試")
	}
	ctx := context.Background()
	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("連線失敗: %v", err)
	}
	var db string
	if err := st.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil {
		st.Close()
		t.Fatalf("取不到資料庫名稱，無法確認這是不是測試庫: %v", err)
	}
	if !strings.HasSuffix(db, testDBSuffix) {
		st.Close()
		t.Fatalf("拒絕在 %q 上跑測試：這些測試會清空 groups / keywords / shops / listings，"+
			"資料庫名稱必須以 %s 結尾。請另外建一個，例如 createdb fbwatch%s",
			db, testDBSuffix, testDBSuffix)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate 失敗: %v", err)
	}
	t.Cleanup(st.Close)
	return st, ctx
}

func sample(id string, price int, status parse.Status) parse.Listing {
	return parse.Listing{
		ID:        id,
		Kind:      "listing",
		Permalink: "https://www.facebook.com/commerce/listing/" + id + "/",
		SellerID:  "100000000000001",
		Text:      "測試貼文 " + id,
		Price:     price,
		NTTag:     price,
		Status:    status,
		RawHTML:   "<div data-virtualized=\"false\"></div>",
	}
}

func TestUpsertReportsNewOnlyOnce(t *testing.T) {
	st, ctx := testStore(t)
	gid := fmt.Sprintf("test-%d", time.Now().UnixNano())
	l := sample(gid+"-a", 300, parse.StatusSelling)
	now := time.Now()

	isNew, err := st.Upsert(ctx, gid, l, now)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Fatal("第一次寫入應回報為新項目")
	}

	isNew, err = st.Upsert(ctx, gid, l, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if isNew {
		t.Fatal("重複寫入不應回報為新項目 —— 去重失效會造成重複通知")
	}
}

func TestPriceHistoryOnlyRecordsChanges(t *testing.T) {
	st, ctx := testStore(t)
	gid := fmt.Sprintf("test-%d", time.Now().UnixNano())
	id := gid + "-b"
	base := time.Now()

	for i, price := range []int{500, 500, 450} {
		if _, err := st.Upsert(ctx, gid, sample(id, price, parse.StatusSelling),
			base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	var n int
	if err := st.pool.QueryRow(ctx,
		`SELECT count(*) FROM price_history WHERE listing_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("price_history 應只記錄 2 筆（500 與 450），實得 %d", n)
	}
}

func TestGroupSeeded(t *testing.T) {
	st, ctx := testStore(t)
	gid := fmt.Sprintf("test-%d", time.Now().UnixNano())

	seeded, err := st.GroupSeeded(ctx, gid)
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("全新社團不應被視為已 seed")
	}

	if _, err := st.Upsert(ctx, gid, sample(gid+"-c", 100, parse.StatusSelling), time.Now()); err != nil {
		t.Fatal(err)
	}
	seeded, err = st.GroupSeeded(ctx, gid)
	if err != nil {
		t.Fatal(err)
	}
	if !seeded {
		t.Fatal("已有資料的社團應被視為已 seed —— 否則會重複發出基準通知")
	}
}

func TestMarkNotified(t *testing.T) {
	st, ctx := testStore(t)
	gid := fmt.Sprintf("test-%d", time.Now().UnixNano())
	id := gid + "-d"
	now := time.Now()

	if _, err := st.Upsert(ctx, gid, sample(id, 200, parse.StatusSelling), now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNotified(ctx, []string{id}, now); err != nil {
		t.Fatal(err)
	}

	var cnt int
	if err := st.pool.QueryRow(ctx,
		`SELECT count(*) FROM listings WHERE id = $1 AND notified_at IS NOT NULL`, id).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatal("MarkNotified 應設定 notified_at")
	}
}

// feed 的相對時間只有小時級精度，推算值可能落在我們第一次看到貼文之後，
// 產生負的 feed_lag。實測出現過 -7 分鐘。
func TestUpsertClampsFuturePostedAt(t *testing.T) {
	st, ctx := testStore(t)
	gid := fmt.Sprintf("test-%d", time.Now().UnixNano())
	now := time.Now().Truncate(time.Second)

	l := sample(gid+"-clamp", 100, parse.StatusSelling)
	l.PostedAt = now.Add(20 * time.Minute) // 推算成未來
	if _, err := st.Upsert(ctx, gid, l, now); err != nil {
		t.Fatal(err)
	}

	var got time.Time
	if err := st.pool.QueryRow(ctx,
		`SELECT posted_at FROM listings WHERE id = $1`, l.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.After(now) {
		t.Fatalf("posted_at = %v，不應晚於 first_seen %v", got, now)
	}
}

func TestUpsertKeepsValidPostedAt(t *testing.T) {
	st, ctx := testStore(t)
	gid := fmt.Sprintf("test-%d", time.Now().UnixNano())
	now := time.Now().Truncate(time.Second)

	l := sample(gid+"-keep", 100, parse.StatusSelling)
	want := now.Add(-2 * time.Hour)
	l.PostedAt = want
	if _, err := st.Upsert(ctx, gid, l, now); err != nil {
		t.Fatal(err)
	}

	var got time.Time
	if err := st.pool.QueryRow(ctx,
		`SELECT posted_at FROM listings WHERE id = $1`, l.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("posted_at = %v, want %v（合理的過去時間不應被動到）", got, want)
	}
}

// 最後一道防線：解析器永遠會有沒想到的輸入，但一個壞掉的數字
// 不該讓寫入失敗、進而觸發熔斷停機。寧可那一欄是 NULL。
func TestNullableIntRejectsOutOfRange(t *testing.T) {
	for _, v := range []int{0, -1, 2610168104526, 1 << 40} {
		if got := nullableInt(v); got != nil {
			t.Errorf("nullableInt(%d) = %d, 應為 nil", v, *got)
		}
	}
	for _, v := range []int{1, 1200, 999999999} {
		got := nullableInt(v)
		if got == nil || *got != v {
			t.Errorf("nullableInt(%d) 不該被擋掉", v)
		}
	}
}

// nil 的排除清單會被當成 SQL 的 NULL，而 `x = ANY(NULL)` 是 NULL 不是 false，
// NOT NULL 還是 NULL，WHERE 就把每一列都濾掉 —— 一則通知都不送而且完全不報錯。
// 這是整個分流功能最危險的一條，所以直接打真的資料庫驗。
func TestPendingNotificationsWithNilSkipListStillReturnsRows(t *testing.T) {
	st, ctx := testStore(t)
	gid := fmt.Sprintf("test-%d", time.Now().UnixNano())
	id := gid + "-nil"

	if _, err := st.Upsert(ctx, gid, sample(id, 300, parse.StatusSelling), time.Now()); err != nil {
		t.Fatal(err)
	}
	// 不比對「有沒有撈到剛插的那筆」—— 佇列是最舊的優先，測試資料庫累積的
	// 待通知列會把它擠到額度外，那是測試自己的脆弱不是程式的問題。
	// bug 的形狀是「一筆都不回」，所以比對的是非空，以及 nil 與空切片等價。
	withNil, err := st.PendingNotifications(ctx, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(withNil) == 0 {
		t.Fatal("排除清單為 nil 時回了 0 筆 —— nil 被當成 SQL 的 NULL，所有通知會靜默全停")
	}
	withEmpty, err := st.PendingNotifications(ctx, 50, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(withNil) != len(withEmpty) {
		t.Errorf("nil 與空切片應該等價，實得 %d 與 %d", len(withNil), len(withEmpty))
	}
	_ = id
}

// 被排除的管道整組不出現在佇列裡，別的社團才不會被一個設錯的 webhook 卡住。
func TestPendingNotificationsExcludesBrokenChannel(t *testing.T) {
	st, ctx := testStore(t)
	now := time.Now()
	stamp := time.Now().UnixNano()
	bad := fmt.Sprintf("test-bad-%d", stamp)
	good := fmt.Sprintf("test-good-%d", stamp)

	for _, g := range []struct{ id, hook string }{{bad, "HOOK_MISSING"}, {good, ""}} {
		if _, err := st.pool.Exec(ctx,
			`INSERT INTO groups (id, webhook_env) VALUES ($1, NULLIF($2,''))`, g.id, g.hook); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = st.pool.Exec(ctx, `DELETE FROM groups WHERE id = $1`, g.id) })
	}
	if _, err := st.Upsert(ctx, bad, sample(bad+"-x", 100, parse.StatusSelling), now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(ctx, good, sample(good+"-x", 100, parse.StatusSelling), now); err != nil {
		t.Fatal(err)
	}

	got, err := st.PendingNotifications(ctx, 200, []string{"HOOK_MISSING"})
	if err != nil {
		t.Fatal(err)
	}
	var sawBad, sawGood bool
	for _, p := range got {
		switch p.ID {
		case bad + "-x":
			sawBad = true
		case good + "-x":
			sawGood = true
		}
	}
	if sawBad {
		t.Error("被排除的管道不該出現在佇列裡")
	}
	if !sawGood {
		t.Error("沒被排除的社團仍然要送得出去 —— 不然一個設定錯誤變成全站故障")
	}
}

// 分流資訊要跟著貼文一起撈出來，否則 sendPending 不知道該送去哪
func TestPendingNotificationsCarriesWebhookEnv(t *testing.T) {
	st, ctx := testStore(t)
	gid := fmt.Sprintf("test-hook-%d", time.Now().UnixNano())
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO groups (id, webhook_env) VALUES ($1, 'HOOK_X')`, gid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(ctx, `DELETE FROM groups WHERE id = $1`, gid) })

	id := gid + "-y"
	if _, err := st.Upsert(ctx, gid, sample(id, 100, parse.StatusSelling), time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := st.PendingNotifications(ctx, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.ID == id {
			if p.WebhookEnv != "HOOK_X" {
				t.Errorf("webhook_env 應為 HOOK_X，實得 %q", p.WebhookEnv)
			}
			return
		}
	}
	t.Fatal("沒撈到剛建的貼文")
}

// watchedSeller 建一個盯著的賣家與他的一則貼文，回傳 listing id。
func watchedSeller(t *testing.T, st *Store, ctx context.Context, label string, summon bool) string {
	t.Helper()
	sid := fmt.Sprintf("seller-%d", time.Now().UnixNano())
	if err := st.UpsertSeller(ctx, sid, "FB 上的顯示名稱"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx,
		`UPDATE sellers SET label = $2, watched = TRUE, summon = $3 WHERE id = $1`,
		sid, label, summon); err != nil {
		t.Fatal(err)
	}
	id := sid + "-post"
	l := sample(id, 100, parse.StatusSelling)
	l.SellerID = sid
	if _, err := st.Upsert(ctx, "test-watch-group", l, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(ctx, `DELETE FROM listings WHERE id = $1`, id)
		_, _ = st.pool.Exec(ctx, `DELETE FROM sellers WHERE id = $1`, sid)
	})
	return id
}

func pendingByID(t *testing.T, st *Store, ctx context.Context, id string) Pending {
	t.Helper()
	got, err := st.PendingNotifications(ctx, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("沒撈到 %s", id)
	return Pending{}
}

func TestPendingNotificationsCarriesSellerWatch(t *testing.T) {
	st, ctx := testStore(t)
	id := watchedSeller(t, st, ctx, "常出絕版品的那個", true)
	p := pendingByID(t, st, ctx, id)
	if !p.SellerWatched || !p.SellerSummon {
		t.Errorf("盯人設定要跟著貼文出來，實得 watched=%v summon=%v", p.SellerWatched, p.SellerSummon)
	}
	// 提示行寫的是你給的稱呼，不是 FB 的顯示名稱 —— label 存在就是為了這個。
	if p.SellerName != "常出絕版品的那個" {
		t.Errorf("稱呼應優先於 FB 顯示名稱，實得 %q", p.SellerName)
	}
}

func TestSellerCannotBeMutedAndWatchedAtOnce(t *testing.T) {
	st, ctx := testStore(t)
	sid := fmt.Sprintf("seller-x-%d", time.Now().UnixNano())
	if err := st.UpsertSeller(ctx, sid, "矛盾"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(ctx, `DELETE FROM sellers WHERE id = $1`, sid) })
	if _, err := st.pool.Exec(ctx,
		`UPDATE sellers SET muted = TRUE, watched = TRUE WHERE id = $1`, sid); err == nil {
		t.Error("同時靜音又盯著是矛盾設定，資料庫就該擋下來；這種錯在執行期只看得到「怎麼沒跳」")
	}
}
