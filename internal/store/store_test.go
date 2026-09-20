package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"fbwatch/internal/parse"
)

// 需要一個可寫的 Postgres。未設 FBWATCH_TEST_DSN 就跳過，
// 讓 `go test ./...` 在沒有資料庫的環境下仍然可跑。
//
//	FBWATCH_TEST_DSN=postgres://fbwatch:pw@localhost:55432/fbwatch?sslmode=disable go test ./internal/store/
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
