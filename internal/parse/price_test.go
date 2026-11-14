package parse

import (
	"strings"
	"testing"
)

func TestChineseToInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"三百", 300},
		{"五十", 50},
		{"十五", 15},
		{"十", 10},
		{"一千二百三十四", 1234},
		{"兩萬", 20000},

		// 口語省略式：尾數落在下一個低位
		{"兩千五", 2500},
		{"一千二", 1200},
		{"三百五", 350},
		{"一萬五", 15000},

		{"", -1},
		{"模型", -1},
	}
	for _, c := range cases {
		if got := ChineseToInt(c.in); got != c.want {
			t.Errorf("ChineseToInt(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestExtractPrice(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			// 社團實際貼文
			name: "中文數字價格",
			body: "一張三百，要的請密我",
			want: 300,
		},
		{
			// 社團實際貼文：500 是出價，60 是運費，11 是版本號
			name: "排除運費與版本號",
			body: "誠心收購 周邊小物 狀況佳 第11版 特典卡兩套組500 匯款後超商取貨+60元",
			want: 500,
		},
		{
			name: "元結尾",
			body: "出清 一盒 850元 可自取",
			want: 850,
		},
		{
			name: "排除運費加價",
			body: "售1200 郵寄+80",
			want: 1200,
		},
		{
			name: "徵求佔位價不應被選中",
			body: "徵求 限量海報 9999 意者私訊 願出2000",
			want: 2000,
		},
		{
			name: "沒有價格",
			body: "請問這款還有貨嗎",
			want: -1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractPrice(c.body); got != c.want {
				t.Errorf("ExtractPrice(%q) = %d, want %d", c.body, got, c.want)
			}
		})
	}
}

func TestDetectStatus(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		ntTag int
		want  Status
	}{
		{"預設出售", "一張三百，要的請密我", 300, StatusSelling},
		{"收購", "誠心收購 周邊小物 狀況佳", WantedSentinel, StatusWanted},
		{"徵求", "徵求 第10版 規則書", 0, StatusWanted},
		{"已售出蓋過其他", "徵求 已售出 感謝", 0, StatusSold},
		{"保留", "保留中 等匯款", 500, StatusReserved},
		{"僅靠 NT$9999 判定徵求", "限量海報 意者私訊", WantedSentinel, StatusWanted},
		{"NT$ 正常值不影響", "限量海報 意者私訊", 800, StatusSelling},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectStatus(c.body, c.ntTag); got != c.want {
				t.Errorf("DetectStatus(%q, %d) = %q, want %q", c.body, c.ntTag, got, c.want)
			}
		})
	}
}

// 2026-10-16：有人在貼文裡貼了一串 2610168104526（看起來是賣貨便的商品編號），
// 它被選成價格寫進資料庫，超過 PostgreSQL integer 上限，寫入連續失敗
// 觸發熔斷，整個 collector 停機半小時。長數字是編號不是價格。
func TestExtractPriceIgnoresOversizedNumbers(t *testing.T) {
	for _, body := range []string{
		"賣貨便訂單 2610168104526 已出貨",
		"商品編號5000000000000016 售1200",
		"手機0912345678 聯絡我 售800",
	} {
		got := ExtractPrice(body)
		if got > MaxPlausiblePrice {
			t.Errorf("%q → %d，超過上限 %d", body, got, MaxPlausiblePrice)
		}
	}
}

// 擋掉長數字之後，同一則裡真正的價格還是要抓得到
func TestExtractPriceStillFindsRealPriceBesideLongNumber(t *testing.T) {
	if got := ExtractPrice("商品編號5000000000000016 售1200元"); got != 1200 {
		t.Errorf("價格 = %d, want 1200", got)
	}
}

// 上限要夠高，不能把真實的高價品誤殺
func TestExtractPriceKeepsExpensiveItems(t *testing.T) {
	if got := ExtractPrice("絕版整組 售價 85000 元"); got != 85000 {
		t.Errorf("價格 = %d, want 85000", got)
	}
}

// 網址裡的數字不是價格。賣貨便連結帶的是商品編號 ——
// 2026-10-16 熔斷那則就是把連結裡的 2610168104526 當成了售價。
func TestExtractPriceIgnoresNumbersInsideURLs(t *testing.T) {
	body := "售 整組周邊 回血 1800元 下面附上賣貨便連結~ https://myship.7-11.com.tw/general/detail/GM0000000000003"
	if got := ExtractPrice(body); got != 1800 {
		t.Errorf("價格 = %d, want 1800（網址裡的數字不該參與）", got)
	}
}

// 排除長數字之後退而抓到網域裡的 "11"（myship.7-11）也是錯的，
// 整段網址都不該參與
func TestExtractPriceIgnoresDomainDigits(t *testing.T) {
	body := "出清 https://myship.7-11.com.tw/general/detail/GM0000000000001 有興趣私訊"
	if got := ExtractPrice(body); got == 11 || got == 7 {
		t.Errorf("抓到網域裡的數字 %d", got)
	}
}

// 遮蔽要保持位移，否則前後文評分會錯位
func TestMaskURLsKeepsOffsets(t *testing.T) {
	body := "售800元 https://example.com/123 謝謝"
	masked := maskURLs(body)
	if len(masked) != len(body) {
		t.Fatalf("長度變了：%d → %d", len(body), len(masked))
	}
	if !strings.Contains(masked, "售800元") {
		t.Errorf("網址以外的文字不該被動到: %q", masked)
	}
	if strings.Contains(masked, "123") {
		t.Errorf("網址裡的數字應被遮掉: %q", masked)
	}
}
