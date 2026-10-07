package myship

import (
	"os"
	"testing"
)

// fixture 取自 2026-10-07 的真實賣場頁，兩件商品外加一件重複出現的。
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
	if len(got) != 2 {
		t.Fatalf("應解析出 2 件（第 3 個是重複），實得 %d", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Fatal("兩件商品的 id 不該相同")
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
	if it.MinPrice() <= 0 {
		t.Errorf("最低價 = %d，應為正數", it.MinPrice())
	}
}

// 一件商品可能有多種規格（不同盒況），比價以最低價為準
func TestMinPricePicksLowestSpec(t *testing.T) {
	it := Item{Specs: []Spec{{Price: 1200}, {Price: 980}, {Price: 0}, {Price: 1500}}}
	if got := it.MinPrice(); got != 980 {
		t.Errorf("MinPrice = %d, want 980", got)
	}
}

func TestMinPriceWithoutSpecs(t *testing.T) {
	if got := (Item{}).MinPrice(); got != -1 {
		t.Errorf("沒有規格時應為 -1，實得 %d", got)
	}
}

// 庫存歸零代表完售，是值得通知的變動
func TestInventorySumsSpecs(t *testing.T) {
	it := Item{Specs: []Spec{{Inventory: 2}, {Inventory: 0}, {Inventory: 3}}}
	if got := it.Inventory(); got != 5 {
		t.Errorf("Inventory = %d, want 5", got)
	}
	if got := (Item{Specs: []Spec{{Inventory: 0}}}).Inventory(); got != 0 {
		t.Errorf("完售應為 0，實得 %d", got)
	}
}

func TestParseIgnoresGarbage(t *testing.T) {
	if got := Parse(`<div data-product="not json"></div><div>no attr</div>`); len(got) != 0 {
		t.Fatalf("壞資料不該產生商品，實得 %d", len(got))
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
