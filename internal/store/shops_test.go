package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fbwatch/internal/myship"
)

func TestDiffItemFirstSightIsNew(t *testing.T) {
	if got := diffItem(false, false, nil, nil, 100, 1); len(got) != 1 || got[0] != "new" {
		t.Errorf("首次出現應只有 new，實得 %v", got)
	}
}

// 賣貨便會把整件賣完的商品從賣場頁藏起來，賣家補了貨又冒出來。
// 對我們看起來是全新商品，但它建檔時間在我們開始追蹤之前 ——
// 報成「新上架」會讓人點進去找不到東西（排在賣場第 108 位）。
func TestDiffItemOldItemReappearingIsRelist(t *testing.T) {
	if got := diffItem(false, true, nil, nil, 2700, 1); len(got) != 1 || got[0] != "relist" {
		t.Errorf("舊商品重新出現應為 relist，實得 %v", got)
	}
}

// 升級後既有商品的 inventory 被清成 NULL，是刻意留的空基準。
// 這一輪不該因此噴出完售通知 —— 那正是 009 要避開的 42 則洗版。
func TestDiffItemWithoutQtyBaselineStaysQuiet(t *testing.T) {
	price := 100
	if got := diffItem(true, false, &price, nil, 100, 0); len(got) != 0 {
		t.Errorf("沒有數量基準時不該產生事件，實得 %v", got)
	}
}

func TestDiffItemQtyTransitions(t *testing.T) {
	price := 100
	for _, c := range []struct {
		name     string
		oldQty   int
		newQty   int
		wantKind string
	}{
		{"賣完", 1, 0, "soldout"},
		{"補貨", 0, 2, "restock"},
		{"賣掉一部分但還有", 3, 1, ""},
	} {
		got := diffItem(true, false, &price, &c.oldQty, price, c.newQty)
		switch {
		case c.wantKind == "" && len(got) != 0:
			t.Errorf("%s: 不該產生事件，實得 %v", c.name, got)
		case c.wantKind != "" && (len(got) != 1 || got[0] != c.wantKind):
			t.Errorf("%s: want %q, 實得 %v", c.name, c.wantKind, got)
		}
	}
}

func shopItem(id string, price, stock, remaining int) myship.Item {
	return myship.Item{
		ShopID: "GMTEST", ID: id, Name: "測試商品 " + id,
		CreatedAt: time.Now(), // 新建的，不是舊商品重新出現
		Specs: []myship.Spec{{
			ID: id + "-1", Price: price, Stock: stock,
			Reserved: stock - remaining, Remaining: remaining,
		}},
	}
}

func syncShop(t *testing.T, st *Store, ctx context.Context, items []myship.Item, at time.Time) {
	t.Helper()
	if _, _, err := st.SyncShopItems(ctx, "GMTEST", items, at); err != nil {
		t.Fatalf("SyncShopItems 失敗: %v", err)
	}
}

// 賣家把商品刪掉也是「賣掉了」——指名預訂品幾乎都走這條路。
// 原本的比對只跑在本輪抓到的商品上，這種變動完全看不到。
func TestRemovedItemProducesEvent(t *testing.T) {
	st, ctx := testStore(t)
	t.Cleanup(func() { cleanShop(t, st, ctx) })
	cleanShop(t, st, ctx)
	if err := st.AddShop(ctx, "GMTEST", "測試賣場", ""); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().Truncate(time.Second)
	syncShop(t, st, ctx, []myship.Item{
		shopItem("A", 100, 1, 1), shopItem("B", 200, 1, 1), shopItem("C", 300, 1, 1),
	}, t0) // 建基準，不產生事件

	// B 被賣家刪掉
	syncShop(t, st, ctx, []myship.Item{shopItem("A", 100, 1, 1), shopItem("C", 300, 1, 1)},
		t0.Add(time.Minute))

	events, err := st.PendingShopEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("應有 1 則事件，實得 %d: %+v", len(events), events)
	}
	if events[0].Kind != "removed" || events[0].ItemID != "B" {
		t.Errorf("want removed/B，實得 %s/%s", events[0].Kind, events[0].ItemID)
	}

	// 同一件不該每輪重複通知
	syncShop(t, st, ctx, []myship.Item{shopItem("A", 100, 1, 1), shopItem("C", 300, 1, 1)},
		t0.Add(2*time.Minute))
	again, err := st.PendingShopEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Errorf("已移除的商品不該重複產生事件，實得 %d 則", len(again))
	}
}

// 頁面只回傳一部分商品時，剩下的會看起來像被刪光。
// 寧可晚一輪偵測到移除，也不要噴出幾十則假通知。
func TestTruncatedFetchDoesNotMarkRemoved(t *testing.T) {
	st, ctx := testStore(t)
	t.Cleanup(func() { cleanShop(t, st, ctx) })
	cleanShop(t, st, ctx)
	if err := st.AddShop(ctx, "GMTEST", "測試賣場", ""); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().Truncate(time.Second)
	var full []myship.Item
	for _, id := range []string{"A", "B", "C", "D", "E"} {
		full = append(full, shopItem(id, 100, 1, 1))
	}
	syncShop(t, st, ctx, full, t0)

	// 只回傳 1 件（五分之一），應判定抓取不完整而報錯
	_, _, err := st.SyncShopItems(ctx, "GMTEST", full[:1], t0.Add(time.Minute))
	if err == nil {
		t.Fatal("抓取不完整時應回報錯誤")
	}
	// 要能用 errors.Is 認出來。呼叫端靠這個把它跟「站台沒反應」「版型變了」
	// 分開 —— 三者的門檻與文案都不同，比對錯誤字串遲早會跟著文案一起壞。
	if !errors.Is(err, ErrIncompleteFetch) {
		t.Errorf("應該包成 ErrIncompleteFetch，實得 %v", err)
	}
	// 訊息要帶得出數字，告警才看得出掉了多少
	if !strings.Contains(err.Error(), "只抓到 1 件") || !strings.Contains(err.Error(), "原有 5 件") {
		t.Errorf("錯誤訊息要說明抓到幾件、原有幾件，實得 %v", err)
	}
	events, err := st.PendingShopEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("不該產生移除事件，實得 %d 則: %+v", len(events), events)
	}
}

func cleanShop(t *testing.T, st *Store, ctx context.Context) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM shop_events WHERE shop_id = 'GMTEST'`,
		`DELETE FROM shop_items  WHERE shop_id = 'GMTEST'`,
		`DELETE FROM shops       WHERE id      = 'GMTEST'`,
	} {
		if _, err := st.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}
