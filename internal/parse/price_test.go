package parse

import "testing"

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
			body: "誠心收購 混沌邪教徒 成品佳 第11版 任務卡兩套組500 匯款後超商取貨+60元",
			want: 500,
		},
		{
			name: "元結尾",
			body: "出清 一盒 850元 可面交",
			want: 850,
		},
		{
			name: "排除運費加價",
			body: "售1200 郵寄+80",
			want: 1200,
		},
		{
			name: "徵求佔位價不應被選中",
			body: "徵求 星際戰士 9999 意者私訊 願出2000",
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
		{"收購", "誠心收購 混沌邪教徒 成品佳", WantedSentinel, StatusWanted},
		{"徵求", "徵求 第10版 規則書", 0, StatusWanted},
		{"已售出蓋過其他", "徵求 已售出 感謝", 0, StatusSold},
		{"保留", "保留中 等匯款", 500, StatusReserved},
		{"僅靠 NT$9999 判定徵求", "星際戰士 意者私訊", WantedSentinel, StatusWanted},
		{"NT$ 正常值不影響", "星際戰士 意者私訊", 800, StatusSelling},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectStatus(c.body, c.ntTag); got != c.want {
				t.Errorf("DetectStatus(%q, %d) = %q, want %q", c.body, c.ntTag, got, c.want)
			}
		})
	}
}
