package notify

import (
	"strings"
	"testing"
)

const url = "https://myship.7-11.com.tw/general/detail/GM26"

func TestShopEventMessageShape(t *testing.T) {
	got := ShopEventMessage("new", "搶劫完美之城", "帝皇之子原體福根", 0, 4066, 0, 1, url)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("應為兩行（訊息與網址），實得 %d 行: %q", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "搶劫完美之城") {
		t.Errorf("第一行應以稱呼開頭: %q", lines[0])
	}
	if !strings.Contains(lines[0], "帝皇之子原體福根") {
		t.Errorf("第一行應含商品名稱: %q", lines[0])
	}
	if lines[1] != "<"+url+">" {
		t.Errorf("第二行應是以 <> 包住的網址: %q", lines[1])
	}
}

// 少了尾綴的話，新上架與完售會長得一模一樣，看不出發生什麼事
func TestShopEventKindsAreDistinguishable(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range []string{"new", "price", "restock", "soldout"} {
		m := ShopEventMessage(k, "店", "同一件商品", 1500, 1200, 1, 0, url)
		if seen[m] {
			t.Fatalf("%q 與其他類型的訊息相同，分不出差別", k)
		}
		seen[m] = true
	}
}

func TestShopEventPriceDirection(t *testing.T) {
	down := ShopEventMessage("price", "店", "商品", 1500, 1200, 1, 1, url)
	up := ShopEventMessage("price", "店", "商品", 1200, 1500, 1, 1, url)
	if !strings.Contains(down, "↓") || !strings.Contains(up, "↑") {
		t.Errorf("漲跌方向應該看得出來:\n%s\n%s", down, up)
	}
}

func TestShopEventFallbacks(t *testing.T) {
	got := ShopEventMessage("new", "  ", "", 0, 100, 0, 1, url)
	if strings.Contains(got, "　　") {
		t.Errorf("缺名稱時不該留下連續空白: %q", got)
	}
}
