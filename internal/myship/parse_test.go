package myship

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// fixture 是賣場頁的結構，商品內容已換成虛構的：
// 兩件未售出、一件重複出現，外加一件完售與一件賣掉一半的。
func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/shop.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 同一件商品在頁面上會出現兩次（清單一次、彈窗一次）。
// 不去重的話每次輪詢都會把它當成兩件，數量與變動偵測全錯。
func TestParseDedupsRepeatedProducts(t *testing.T) {
	got := Parse(fixture(t))
	if len(got) != 4 {
		t.Fatalf("應解析出 4 件（有一個是重複的），實得 %d", len(got))
	}
	seen := map[string]bool{}
	for _, it := range got {
		if seen[it.ID] {
			t.Fatalf("商品 %s 重複出現", it.ID)
		}
		seen[it.ID] = true
	}
}

func TestParseExtractsFields(t *testing.T) {
	items := Parse(fixture(t))
	it := items[0]

	if it.ShopID == "" || it.ID == "" {
		t.Fatalf("賣場或商品 id 缺失: %+v", it)
	}
	if it.Name == "" {
		t.Error("商品名稱缺失")
	}
	if it.CreatedAt.IsZero() {
		t.Error("上架時間缺失 —— 那是賣貨便比 FB 好處理的關鍵，不該漏")
	}
	if len(it.Specs) == 0 {
		t.Fatal("規格缺失")
	}
	if it.Price() <= 0 {
		t.Errorf("售價 = %d，應為正數", it.Price())
	}
}

// 一件商品可能有多種規格（不同盒況），比價以買得到的最低價為準
func TestMinPricePicksLowestSpec(t *testing.T) {
	it := Item{Specs: []Spec{
		{Price: 1200, Remaining: 1}, {Price: 980, Remaining: 2},
		{Price: 0, Remaining: 1}, {Price: 1500, Remaining: 1},
	}}
	if got := it.MinPrice(); got != 980 {
		t.Errorf("MinPrice = %d, want 980", got)
	}
}

func TestMinPriceWithoutSpecs(t *testing.T) {
	if got := (Item{}).MinPrice(); got != -1 {
		t.Errorf("沒有規格時應為 -1，實得 %d", got)
	}
}

// 剩餘量歸零代表完售，是值得通知的變動
func TestRemainingSumsSpecs(t *testing.T) {
	it := Item{Specs: []Spec{{Remaining: 2}, {Remaining: 0}, {Remaining: 3}}}
	if got := it.Remaining(); got != 5 {
		t.Errorf("Remaining = %d, want 5", got)
	}
	if got := (Item{Specs: []Spec{{Remaining: 0}}}).Remaining(); got != 0 {
		t.Errorf("完售應為 0，實得 %d", got)
	}
}

// 這是開台到現在一次都沒發出完售通知的原因：讀錯欄位。
// Cgds_Inventory 是賣家填的上架量，有人買走時它動都不動；
// Inventory 才會跟著降。沒有這個測試，同樣的誤讀會再回來一次。
func TestRemainingIsNotStock(t *testing.T) {
	byID := map[string]Item{}
	for _, it := range Parse(fixture(t)) {
		byID[it.ID] = it
	}

	sold, ok := byID["5000000000000011"] // 已完售的那件：上架 1、已訂 1
	if !ok {
		t.Fatal("fixture 裡少了那件完售的商品")
	}
	if sold.Stock() != 1 {
		t.Errorf("原始上架量 = %d, want 1", sold.Stock())
	}
	if sold.Remaining() != 0 {
		t.Errorf("剩餘量 = %d, want 0（已完售）", sold.Remaining())
	}

	part, ok := byID["5000000000000005"] // 賣掉一半的那件：上架 2、已訂 1
	if !ok {
		t.Fatal("fixture 裡少了那件賣掉一半的商品")
	}
	if part.Stock() != 2 || part.Remaining() != 1 {
		t.Errorf("上架 %d／剩餘 %d, want 2／1", part.Stock(), part.Remaining())
	}
}

// 萬一賣貨便哪天不給 Inventory 了，用上架量減已訂的補，
// 不要默默當成滿庫存 —— 那會讓完售通知再次全部消失。
func TestRemainingFallsBackToStockMinusReserved(t *testing.T) {
	const doc = `<div data-product="{&quot;Cgdd_Id&quot;:&quot;X&quot;,&quot;Spec&quot;:` +
		`[{&quot;Cgds_Inventory&quot;:5,&quot;Cgds_Qty_Reg&quot;:5}]}"></div>`
	items := Parse(doc)
	if len(items) != 1 {
		t.Fatalf("應解析出 1 件，實得 %d", len(items))
	}
	if got := items[0].Remaining(); got != 0 {
		t.Errorf("缺 Inventory 欄位時剩餘量 = %d, want 0", got)
	}
}

func TestParseIgnoresGarbage(t *testing.T) {
	if got := Parse(`<div data-product="not json"></div><div>no attr</div>`); len(got) != 0 {
		t.Fatalf("壞資料不該產生商品，實得 %d", len(got))
	}
}

// 商品沒有自己的頁面，但卡片的 <a> 帶著 id="<商品id>"，
// 用 # 片段就能讓瀏覽器捲到它 —— 這是通知唯一能給出的對應網址
func TestItemURLAnchorsToProduct(t *testing.T) {
	got := ItemURL("GM26", "700000000000001")
	if got != "https://myship.7-11.com.tw/general/detail/GM26#700000000000001" {
		t.Errorf("ItemURL = %q", got)
	}
	// 沒有商品 id 時退回賣場頁，不要留一個空的 #
	if got := ItemURL("GM26", ""); got != ShopURL("GM26") {
		t.Errorf("缺商品 id 時應退回賣場頁，實得 %q", got)
	}
}

func TestShopAndImageURL(t *testing.T) {
	if got := ShopURL("GM26"); got != "https://myship.7-11.com.tw/general/detail/GM26" {
		t.Errorf("ShopURL = %q", got)
	}
	if got := ImageURL("GM26", "a.jpg"); got != "https://myship.7-11.com.tw/i/cgdm/GM26/a.jpg" {
		t.Errorf("ImageURL = %q", got)
	}
	if got := ImageURL("GM26", ""); got != "" {
		t.Errorf("沒有圖片時應回空字串，實得 %q", got)
	}
}

// 「處決者型反重力坦克」同時掛著完整盒裝 $2700 與水貼 $30。
// 水貼賣掉之後若還拿 $30 當售價，通知會跟實際價格差快一百倍 ——
// 2026-10-08 真的這樣發出去過一次。
func TestMinPriceIgnoresSoldOutSpecs(t *testing.T) {
	it := Item{Specs: []Spec{
		{Price: 2700, Remaining: 1},
		{Price: 2750, Remaining: 0},
		{Price: 30, Remaining: 0}, // 賣掉的水貼
	}}
	if got := it.MinPrice(); got != 2700 {
		t.Errorf("買得到的最低價 = %d, want 2700", got)
	}
	if got := it.LastPrice(); got != 30 {
		t.Errorf("不分庫存的最低價 = %d, want 30", got)
	}
	if got := it.Price(); got != 2700 {
		t.Errorf("售價 = %d, want 2700", got)
	}
}

// 整件賣完時沒有「買得到的價格」，但通知裡還是要有數字
func TestPriceFallsBackWhenNothingLeft(t *testing.T) {
	it := Item{Specs: []Spec{{Price: 980, Remaining: 0}, {Price: 1200, Remaining: 0}}}
	if got := it.MinPrice(); got != -1 {
		t.Errorf("全部賣完時應為 -1，實得 %d", got)
	}
	if got := it.Price(); got != 980 {
		t.Errorf("售價應退回最後的價格 980，實得 %d", got)
	}
}

func TestPriceWithoutSpecs(t *testing.T) {
	if got := (Item{}).Price(); got != -1 {
		t.Errorf("沒有規格時應為 -1，實得 %d", got)
	}
}

// 賣家把賣場關掉時頁面照樣回 HTTP 200，只有內文會說。
// 這要跟「版型變了」分開：版型是我們要修的程式問題，
// 賣場關了是對方的決定，重試一萬次也不會好。
func TestIsOfflineRecognisesTakedownPage(t *testing.T) {
	// 取自 2026-10-27 的真實回應
	const page = `7-ELEVEN 賣貨便 此賣場已由賣家自行下架，目前無法販售及購買商品 ` +
		`訊息代碼: 0102M1 時間: 2026-10-27 17:46:57。Code：102 回首頁`
	if !isOffline(page) {
		t.Error("沒有認出下架頁")
	}
	if len(Parse(page)) != 0 {
		t.Error("下架頁不該解析出商品")
	}
	// 文案改了還有代碼可以認，反之亦然
	if !isOffline("訊息代碼: 0102M1") || !isOffline("此賣場已由賣家自行下架") {
		t.Error("兩個標記應該各自都認得出來")
	}
}

func TestIsOfflineIgnoresNormalPage(t *testing.T) {
	if isOffline(fixture(t)) {
		t.Error("正常賣場頁被誤判成下架")
	}
	if isOffline("") {
		t.Error("空字串不該被當成下架")
	}
}

// 版型變了與賣場下架是兩種錯誤，不能混在一起 ——
// 一個要修程式，一個要停止追蹤
func TestOfflineErrorIsDistinct(t *testing.T) {
	if errors.Is(fmt.Errorf("頁面解析不出任何商品（123 位元組）"), ErrShopOffline) {
		t.Error("版型錯誤不該被當成賣場下架")
	}
	if !errors.Is(fmt.Errorf("抓取失敗: %w", ErrShopOffline), ErrShopOffline) {
		t.Error("包起來之後應該仍然認得出來")
	}
}

// 5xx 與連線失敗要歸成站台類，才能跟「版型變了」分開處置。
func TestFetchClassifiesServerErrorAsSiteUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	f := &Fetcher{HTTP: srv.Client()}
	_, err := f.fetchURL(context.Background(), srv.URL)
	if !errors.Is(err, ErrSiteUnavailable) {
		t.Fatalf("503 應該是站台類，實得 %v", err)
	}
}

// 4xx 不歸站台類：下架的賣場照樣回 200，所以 404 代表網址規則變了，該看一眼。
func TestFetchDoesNotClassifyClientErrorAsSiteUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	f := &Fetcher{HTTP: srv.Client()}
	_, err := f.fetchURL(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("404 應該要是錯誤")
	}
	if errors.Is(err, ErrSiteUnavailable) {
		t.Errorf("404 不該歸成站台類: %v", err)
	}
}

// 連不上（埠沒人聽）也是站台類
func TestFetchClassifiesDialFailureAsSiteUnavailable(t *testing.T) {
	f := &Fetcher{HTTP: &http.Client{Timeout: 2 * time.Second}}
	_, err := f.fetchURL(context.Background(), "http://127.0.0.1:1/")
	if !errors.Is(err, ErrSiteUnavailable) {
		t.Fatalf("連不上應該是站台類，實得 %v", err)
	}
}

// 兩個 sentinel 不能互相誤判：賣家下架要停止追蹤，站台沒反應只要等。
func TestSiteUnavailableIsDistinctFromShopOffline(t *testing.T) {
	if errors.Is(ErrSiteUnavailable, ErrShopOffline) || errors.Is(ErrShopOffline, ErrSiteUnavailable) {
		t.Error("兩種錯誤必須分得開")
	}
}

func TestConditionLabel(t *testing.T) {
	for in, want := range map[string]string{"1": "新品", "2": "二手", "": "", "9": ""} {
		if got := ConditionLabel(in); got != want {
			t.Errorf("ConditionLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
