package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"fbwatch/internal/keyword"
	"fbwatch/internal/myship"
	"fbwatch/internal/parse"
	"fbwatch/internal/store"
)

// fakeStore 讓指定的貼文寫入失敗，其他照常成功
type fakeStore struct {
	failOn     map[string]bool
	failSeller map[string]bool
	allFail    bool
	seen       []string
}

var errBoom = errors.New("int4 塞不下")

func (f *fakeStore) UpsertSeller(_ context.Context, id, _ string) error {
	if f.allFail || f.failSeller[id] {
		return errBoom
	}
	return nil
}

func (f *fakeStore) Upsert(_ context.Context, _ string, l parse.Listing, _ time.Time) (bool, error) {
	if f.allFail || f.failOn[l.ID] {
		return false, errBoom
	}
	f.seen = append(f.seen, l.ID)
	return true, nil
}

func listings(ids ...string) []parse.Listing {
	var out []parse.Listing
	for _, id := range ids {
		out = append(out, parse.Listing{ID: id, SellerID: "s-" + id})
	}
	return out
}

func TestStoreItemsAllSucceed(t *testing.T) {
	f := &fakeStore{}
	stored, newIDs, failed, err := storeItems(context.Background(), f, "g", listings("a", "b", "c"), time.Now())
	if err != nil {
		t.Fatalf("不該有錯: %v", err)
	}
	if stored != 3 || len(newIDs) != 3 || failed != 0 {
		t.Errorf("stored=%d newIDs=%d failed=%d", stored, len(newIDs), failed)
	}
}

// 這就是 2026-10-16 停機的情境：一篇貼文的價格撐爆 int4。
// 它該被跳過，其他的照常寫入，整輪不算失敗 —— 不然熔斷會把整台停掉。
func TestStoreItemsSkipsOneBadListing(t *testing.T) {
	f := &fakeStore{failOn: map[string]bool{"bad": true}}
	stored, newIDs, failed, err := storeItems(context.Background(), f, "g", listings("a", "bad", "c"), time.Now())
	if err != nil {
		t.Fatalf("一則壞掉不該讓整輪失敗: %v", err)
	}
	if stored != 2 || failed != 1 {
		t.Errorf("stored=%d failed=%d, want 2/1", stored, failed)
	}
	if len(newIDs) != 2 {
		t.Errorf("成功的兩則都該列入新增，實得 %d", len(newIDs))
	}
	for _, id := range f.seen {
		if id == "bad" {
			t.Error("壞掉那則不該被寫進去")
		}
	}
}

// 賣家寫入失敗也只跳過那一則
func TestStoreItemsSkipsBadSeller(t *testing.T) {
	f := &fakeStore{failSeller: map[string]bool{"s-bad": true}}
	stored, _, failed, err := storeItems(context.Background(), f, "g", listings("a", "bad"), time.Now())
	if err != nil || stored != 1 || failed != 1 {
		t.Errorf("stored=%d failed=%d err=%v", stored, failed, err)
	}
}

// 全部失敗是另一回事：資料庫掛了、連線斷了。
// 這種要往上報，熔斷才接得到真正該停下來的狀況。
func TestStoreItemsReportsTotalFailure(t *testing.T) {
	f := &fakeStore{allFail: true}
	stored, _, failed, err := storeItems(context.Background(), f, "g", listings("a", "b"), time.Now())
	if err == nil {
		t.Fatal("全部失敗時應回報錯誤")
	}
	if stored != 0 || failed != 2 {
		t.Errorf("stored=%d failed=%d", stored, failed)
	}
	if !errors.Is(err, errBoom) {
		t.Error("應包住原始錯誤，不然看不出為什麼掛")
	}
	if !strings.Contains(err.Error(), "全部寫入失敗") {
		t.Errorf("錯誤訊息要說清楚: %v", err)
	}
}

// 沒有貼文不是錯誤，只是這輪沒東西
func TestStoreItemsEmptyIsNotAnError(t *testing.T) {
	f := &fakeStore{allFail: true}
	stored, _, failed, err := storeItems(context.Background(), f, "g", nil, time.Now())
	if err != nil || stored != 0 || failed != 0 {
		t.Errorf("stored=%d failed=%d err=%v", stored, failed, err)
	}
}

// 單次失敗很常見（連線抖動、對方重啟），每次都叫會變成雜訊而沒人看；
// 但一直失敗下去也不該安靜 —— 一個賣場連續失敗十天沒人發現就是這樣。
func TestShopFailuresAlertsOnceAtThreshold(t *testing.T) {
	f := newShopFailures()
	for i := 1; i < shopAlertAfter; i++ {
		if f.fail("A", shopAlertAfter) {
			t.Fatalf("第 %d 次就告警，太早了", i)
		}
	}
	if !f.fail("A", shopAlertAfter) {
		t.Fatalf("第 %d 次應該告警", shopAlertAfter)
	}
	for i := 0; i < 20; i++ {
		if f.fail("A", shopAlertAfter) {
			t.Fatal("同一次故障不該重複告警")
		}
	}
}

// 恢復之後再壞，還是要再叫一次 —— 不然第二次故障會被第一次的旗標蓋掉
func TestShopFailuresResetsAfterSuccess(t *testing.T) {
	f := newShopFailures()
	for i := 0; i < shopAlertAfter; i++ {
		f.fail("A", shopAlertAfter)
	}
	f.ok("A")
	for i := 1; i < shopAlertAfter; i++ {
		if f.fail("A", shopAlertAfter) {
			t.Fatalf("歸零後第 %d 次就告警，太早了", i)
		}
	}
	if !f.fail("A", shopAlertAfter) {
		t.Error("恢復之後再壞應該再告警一次")
	}
}

// 各賣場各算各的，一個壞掉不該影響另一個的計數
func TestShopFailuresAreIndependent(t *testing.T) {
	f := newShopFailures()
	for i := 0; i < shopAlertAfter; i++ {
		f.fail("A", shopAlertAfter)
	}
	if f.fail("B", shopAlertAfter) {
		t.Error("B 才失敗一次，不該告警")
	}
}

// 站台沒反應跟版型壞了的處置相反：前者等就好，後者要改程式。
// 標題指錯方向的話，每則告警都得讀第二行才知道該不該理。
func TestShopAlertSeparatesSiteOutageFromLayoutChange(t *testing.T) {
	site := fmt.Errorf("%w: HTTP 503", myship.ErrSiteUnavailable)
	after, headline := shopAlert(site)
	if after != shopAlertAfterSite {
		t.Errorf("站台類該用寬鬆門檻 %d，實得 %d", shopAlertAfterSite, after)
	}
	if strings.Contains(headline, "版型") {
		t.Errorf("站台沒反應不該說版型變了: %q", headline)
	}

	layout := errors.New("頁面解析不出任何商品（12345 位元組），版型可能已變更")
	after, headline = shopAlert(layout)
	if after != shopAlertAfter {
		t.Errorf("版型類該用嚴格門檻 %d，實得 %d", shopAlertAfter, after)
	}
	if !strings.Contains(headline, "版型") {
		t.Errorf("解析不出商品該指向版型: %q", headline)
	}
}

// 站台類的門檻要真的比較寬：10 分鐘的維護不該叫人起來。
func TestSiteOutageStaysQuietBelowItsThreshold(t *testing.T) {
	if shopAlertAfterSite <= shopAlertAfter {
		t.Fatalf("站台類門檻 %d 應該比版型類 %d 寬", shopAlertAfterSite, shopAlertAfter)
	}
	f := newShopFailures()
	for i := 1; i < shopAlertAfterSite; i++ {
		if f.fail("A", shopAlertAfterSite) {
			t.Fatalf("站台類第 %d 次就告警，太早了", i)
		}
	}
	if !f.fail("A", shopAlertAfterSite) {
		t.Errorf("第 %d 次應該告警", shopAlertAfterSite)
	}
}

// 連續失敗中途錯誤類型換了（503 之後變成解析失敗），計數不該歸零 ——
// 歸零的話一直在兩類之間跳動的故障會永遠叫不出來。
func TestShopFailuresKeepCountingAcrossErrorClasses(t *testing.T) {
	f := newShopFailures()
	for i := 1; i < shopAlertAfter; i++ {
		f.fail("A", shopAlertAfterSite) // 先當站台類累計，都還不到寬門檻
	}
	if !f.fail("A", shopAlertAfter) {
		t.Error("改用嚴格門檻時，先前累計的次數應該算進去")
	}
}

// 過濾的不對稱是這個功能的核心：命中就馬上送，沒命中要看內文定案了沒。
//
// feed 上的截斷內文可能根本還沒講到關鍵字（實測有貼文把品項列在第二段），
// 這時候判死會漏掉真正想要的貼文。漏掉比晚一輪嚴重得多。
func TestAllowSendsImmediatelyOnMatch(t *testing.T) {
	f := keyword.NewFilter([]string{"顏料"})
	// 內文還沒定案也照送 —— 已經命中了，等下去只是增加延遲
	send, decided := allow(f, "出售水性漆顏料一批", false)
	if !send || !decided {
		t.Errorf("命中就該馬上送並定案，實得 send=%v decided=%v", send, decided)
	}
}

func TestAllowHoldsWhenBodyNotFinal(t *testing.T) {
	f := keyword.NewFilter([]string{"顏料"})
	send, decided := allow(f, "清倉拍賣", false)
	if send {
		t.Error("沒命中不該送")
	}
	if decided {
		t.Error("內文還可能變豐富時不該定案 —— 下一輪補到完整內文要重判")
	}
}

func TestAllowDropsWhenBodyIsFinal(t *testing.T) {
	f := keyword.NewFilter([]string{"顏料"})
	send, decided := allow(f, "清倉拍賣", true)
	if send {
		t.Error("沒命中不該送")
	}
	if !decided {
		t.Error("內文已定案就該判死，否則會永遠留在佇列裡佔額度")
	}
}

// 沒設過濾的社團完全不受影響
func TestAllowPassesEverythingWithoutPatterns(t *testing.T) {
	f := keyword.NewFilter(nil)
	for _, final := range []bool{true, false} {
		send, decided := allow(f, "今天天氣真好，隨手拍的夕陽", final)
		if !send || !decided {
			t.Errorf("沒設過濾應該一律放行（final=%v）", final)
		}
	}
}

// 日誌要看得出濾掉了什麼。呼叫端傳的是 標題+"\n"+內文，
// 沒標題時第一行是空的，只取第一行會印出空白。
func TestFirstLineSkipsBlankLeadingLines(t *testing.T) {
	if got := firstLine("\n\n  \n今天天氣真好"); got != "今天天氣真好" {
		t.Errorf("實得 %q", got)
	}
	if got := firstLine("   \n  "); got != "(無內文)" {
		t.Errorf("全空白應該回可辨識的字串，實得 %q", got)
	}
}

// 截斷跟另外兩類的方向相反：其他失敗是「沒拿到資料」，截斷是「拿到了但主動丟掉」。
// 丟掉期間所有商品變動都被擋住，包含真實的完售與下架，所以門檻要最嚴。
func TestShopAlertTreatsIncompleteFetchAsItsOwnClass(t *testing.T) {
	err := fmt.Errorf("%w：賣場 GMX 這輪只抓到 3 件、原有 20 件", store.ErrIncompleteFetch)
	after, headline := shopAlert(err)
	if after != shopAlertAfterTrunc {
		t.Errorf("截斷類門檻應為 %d，實得 %d", shopAlertAfterTrunc, after)
	}
	if strings.Contains(headline, "版型") || strings.Contains(headline, "站台沒反應") {
		t.Errorf("截斷不該被歸成另外兩類: %q", headline)
	}
}

// 三類的門檻關係：截斷最嚴 < 版型 < 站台最寬。
// 順序錯了會讓最該吵的那一類反而最晚講話。
func TestShopAlertThresholdOrdering(t *testing.T) {
	if !(shopAlertAfterTrunc < shopAlertAfter && shopAlertAfter < shopAlertAfterSite) {
		t.Errorf("門檻順序應為 截斷(%d) < 版型(%d) < 站台(%d)",
			shopAlertAfterTrunc, shopAlertAfter, shopAlertAfterSite)
	}
}

// 截斷要能累積。原本的寫法在抓取成功後就把計數歸零，
// 之後才做同步 —— 截斷永遠到不了門檻，等於這個告警不存在。
func TestIncompleteFetchAccumulatesAcrossCycles(t *testing.T) {
	f := newShopFailures()
	for i := 1; i < shopAlertAfterTrunc; i++ {
		if f.fail("GMX", shopAlertAfterTrunc) {
			t.Fatalf("第 %d 輪就告警，太早了", i)
		}
	}
	if !f.fail("GMX", shopAlertAfterTrunc) {
		t.Errorf("連續 %d 輪截斷應該告警", shopAlertAfterTrunc)
	}
}
