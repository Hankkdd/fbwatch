package parse

import (
	"strings"
	"testing"
	"time"
)

// 取自 2026-09-20 實際詳情頁的內嵌 JSON 片段。
// 中文在 JSON 裡是 \uXXXX 跳脫，這正是不能用正則抓的原因。
const listingJSON = `<script type="application/json" data-sjs>{"redacted_description": {"text": "\u8aa0\u5fc3\u6536\u8cfc \u6df7\u6c8c\u90aa\u6559\u5f92 \u6210\u54c1\u4f73\n\n\u7b2c11\u7248 \u4efb\u52d9\u5361\u5169\u5957\u7d44500 \n\n\u532f\u6b3e\u5f8c\u8d85\u5546\u53d6\u8ca8+60\u5143\n\u4e5f\u53ef\u6843\u5712\u4e2d\u58e2\u53ef\u9762\u4ea4"}, "creation_time": 1789862784, "location_text": {"text": "\u6843\u5712\u5e02, \u53f0\u7063"}, "listing_price": {"formatted_amount_zeros_stripped": "NT$9,999", "amount": "9999", "currency": "TWD"}}</script>`

func TestListingPage(t *testing.T) {
	d, ok := ListingPage(listingJSON)
	if !ok {
		t.Fatal("應成功取出描述")
	}

	// feed 上只到「任務卡兩套組500」就被截斷，詳情頁的 JSON 才有後面兩段
	if !contains(d.Description, "也可桃園中壢可面交") {
		t.Errorf("描述不完整: %q", d.Description)
	}
	if !contains(d.Description, "匯款後超商取貨+60元") {
		t.Errorf("描述缺少中段: %q", d.Description)
	}

	want := time.Unix(1789862784, 0)
	if !d.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", d.CreatedAt, want)
	}
	if d.Location != "桃園市, 台灣" {
		t.Errorf("Location = %q", d.Location)
	}
	if d.Price != 9999 {
		t.Errorf("Price = %d, want 9999（這是標價欄位，不是內文價格）", d.Price)
	}
	if d.Currency != "TWD" {
		t.Errorf("Currency = %q", d.Currency)
	}
}

func TestListingPageRejectsUnrelatedHTML(t *testing.T) {
	if _, ok := ListingPage("<html><body>沒有商品資料</body></html>"); ok {
		t.Error("沒有描述時應回報失敗，否則會把空內文寫進資料庫覆蓋掉截斷版")
	}
}

// 完整內文讓價格判定變準：feed 的截斷版看不到「也可桃園中壢可面交」，
// 也可能漏掉真正的售價。
func TestFullBodyImprovesPrice(t *testing.T) {
	d, ok := ListingPage(listingJSON)
	if !ok {
		t.Fatal("應成功取出描述")
	}
	if got := ExtractPrice(d.Description); got != 500 {
		t.Errorf("ExtractPrice(完整內文) = %d, want 500", got)
	}
	if got := DetectStatus(d.Description, d.Price); got != StatusWanted {
		t.Errorf("DetectStatus = %q, want wanted", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// 貼文頁裡 `"message":` 會出現多次且多半是 null。只比對鍵名會解出空字串，
// 而且表現為「補抓成功但 0 字」—— 看起來正常，其實資料是空的。
func TestListingPageFindsPostMessageAmongDecoys(t *testing.T) {
	d, ok := ListingPage(postFixture)
	if !ok {
		t.Fatal("應辨識為可用頁面")
	}
	if !strings.Contains(d.Description, "戰爭頭目") {
		t.Fatalf("內文未取到，實得 %q", d.Description)
	}
	if d.CreatedAt.IsZero() {
		t.Error("貼文頁也該取得 creation_time")
	}
}

const postFixture = `<script type="application/json" data-sjs>{"message":null,"other":1}{"message":{"text":"1\u3001\u6230\u722d\u982d\u76ee\uff1a400\n2\u3001\u5927\u982d\u76ee+\u65d7\u624b+\u75db\u82e6\u5c0f\u5b50\uff1a900\n3\u3001\u9748\u80fd\u5c0f\u5b50\uff1a400"},"creation_time":1789900000}</script>`
