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

// listing_photos 的結構取自 2026-09-21 的實際詳情頁。
// 一則貼文十幾張圖很常見，順序要保留。
func TestListingPagePhotos(t *testing.T) {
	d, ok := ListingPage(photosFixture)
	if !ok {
		t.Fatal("應辨識為可用頁面")
	}
	if len(d.Photos) != 2 {
		t.Fatalf("應取到 2 張，實得 %d", len(d.Photos))
	}
	if !strings.HasPrefix(d.Photos[0].URL, "https://scontent.") {
		t.Errorf("第一張網址 = %q", d.Photos[0].URL)
	}
	if d.Photos[0].Width != 726 || d.Photos[0].Height != 960 {
		t.Errorf("尺寸 = %dx%d, want 726x960", d.Photos[0].Width, d.Photos[0].Height)
	}
	// FB 自動生成的描述含辨識出的文字，是之後關鍵字搜尋的免費索引來源
	if !strings.Contains(d.Photos[0].Caption, "WARHAMMER") {
		t.Errorf("caption 未取到: %q", d.Photos[0].Caption)
	}
	if d.Photos[1].ID != "10165206110847422" {
		t.Errorf("順序或 ID 有誤: %q", d.Photos[1].ID)
	}
}

const photosFixture = `<script type="application/json" data-sjs>{"listing_photos":[{"__typename":"Photo","accessibility_caption":"\u53ef\u80fd\u662f\u986f\u793a\u7684\u6587\u5b57\u662f\u300cWARHAMMER 40,000 DARK ANGELS\u300d\u7684\u5716\u50cf","image":{"height":960,"width":726,"uri":"https://scontent.ftpe7-2.fna.fbcdn.net/v/t39.30808-6/aaa.jpg?_nc_cat=1&oh=xx"},"id":"10165206110847421"},{"__typename":"Photo","accessibility_caption":"\u53ef\u80fd\u662f\u73a9\u5177\u7684\u5716\u50cf","image":{"height":766,"width":960,"uri":"https://scontent.ftpe7-1.fna.fbcdn.net/v/t39.30808-6/bbb.jpg?_nc_cat=2&oh=yy"},"id":"10165206110847422"}],"redacted_description":{"text":"\u6e2c\u8a66\u5546\u54c1"},"creation_time":1789900000}</script>`

// 貼文頁的照片在 media.photo_image，與商品頁的 listing_photos 是兩套結構。
// 頁面裡同時有大頭貼與圖示，必須靠尺寸下限濾掉 —— 實測誤抓過 80x80 的大頭貼。
func TestExtractPhotosFromPostPage(t *testing.T) {
	d, ok := ListingPage(postPhotoFixture)
	if !ok {
		t.Fatal("應辨識為可用頁面")
	}
	if len(d.Photos) != 2 {
		var got []string
		for _, p := range d.Photos {
			got = append(got, p.URL)
		}
		t.Fatalf("應取到 2 張大圖，實得 %d：%v", len(d.Photos), got)
	}
	for _, p := range d.Photos {
		if strings.Contains(p.URL, "avatar") || strings.Contains(p.URL, "icon") {
			t.Errorf("不該抓到大頭貼或圖示：%s", p.URL)
		}
	}
}

// 同一張圖會在不同渲染脈絡重複出現，query 帶不同簽章，要用路徑去重
func TestExtractPhotosDedupsBySamePath(t *testing.T) {
	d, _ := ListingPage(postPhotoFixture)
	paths := map[string]bool{}
	for _, p := range d.Photos {
		paths[strings.Split(p.URL, "?")[0]] = true
	}
	if len(paths) != len(d.Photos) {
		t.Fatalf("有重複路徑未去重：%d 張但只有 %d 個路徑", len(d.Photos), len(paths))
	}
}

const postPhotoFixture = `<script type="application/json" data-sjs>{"media":{"__typename":"Photo","photo_image":{"height":800,"uri":"https://scontent.example/v/big1.jpg?oh=aa","width":774},"id":"111"},"profile_picture":{"uri":"https://scontent.example/v/avatar.jpg?oh=bb","width":80,"height":80},"message":{"text":"\u6e2c\u8a66\u8cbc\u6587"},"creation_time":1789900000}{"media":{"__typename":"Photo","photo_image":{"height":600,"uri":"https://scontent.example/v/big2.png?oh=cc","width":900},"id":"222"},"dup":{"photo_image":{"height":800,"uri":"https://scontent.example/v/big1.jpg?oh=DIFFERENT","width":774}},"icon":{"photo_image":{"height":16,"uri":"https://scontent.example/v/icon.png","width":16}}}</script>`
