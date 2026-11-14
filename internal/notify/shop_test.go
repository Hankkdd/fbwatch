package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

const url = "https://myship.7-11.com.tw/general/detail/GM26"
const img = "https://myship.7-11.com.tw/i/cgdm/GM26/a.jpg"

func ev(kind string) ShopEvent {
	return ShopEvent{
		Kind: kind, ShopName: "範例賣場", ItemName: "限量版海報",
		OldPrice: 1500, NewPrice: 1200, OldQty: 3, NewQty: 1,
		ShopURL: url, ImageURL: img,
	}
}

// 推播文字要能在通知列上直接判斷，所以稱呼與商品名稱都得在。
// 分兩行是因為三段擠一行時商品名稱夾在中間，手機寬度下會亂折行。
func TestShopEventMessageShape(t *testing.T) {
	lines := strings.Split(ShopEventMessage(ev("new")), "\n")
	if len(lines) != 3 {
		t.Fatalf("應為三行，實得 %d 行: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "範例賣場") {
		t.Errorf("第一行應以稱呼開頭: %q", lines[0])
	}
	if !strings.Contains(lines[0], "新上架") {
		t.Errorf("第一行應寫出發生什麼: %q", lines[0])
	}
	if lines[1] != "限量版海報" {
		t.Errorf("第二行應是商品名稱本身: %q", lines[1])
	}
}

// 商品名稱獨佔一行，不要再跟別的東西擠
func TestShopEventMessageItemLineIsAlone(t *testing.T) {
	for _, k := range []string{"new", "relist", "price", "restock", "soldout", "removed"} {
		lines := strings.Split(ShopEventMessage(ev(k)), "\n")
		if len(lines) != 3 || lines[1] != "限量版海報" {
			t.Errorf("%s: 第二行應只有商品名稱，實得 %q", k, lines)
		}
	}
}

// 網址要在 content 裡，bot 才查得到是哪一件；但要用 <> 包住，
// 不然 Discord 會另外長一個賣場預覽，跟我們自己的卡片重複。
func TestShopEventMessageURLIsLastAndSuppressed(t *testing.T) {
	e := ev("new")
	lines := strings.Split(ShopEventMessage(e), "\n")
	last := lines[len(lines)-1]
	if last != "<"+e.ShopURL+">" {
		t.Errorf("最後一行應是用 <> 包住的網址，實得 %q", last)
	}
	for _, l := range lines[:len(lines)-1] {
		if strings.Contains(l, "http") {
			t.Errorf("網址只能出現在最後一行，免得吃掉推播列: %q", l)
		}
	}
}

// 少了尾綴的話，新上架與完售會長得一模一樣，看不出發生什麼事
func TestShopEventKindsAreDistinguishable(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range []string{"new", "relist", "price", "restock", "soldout", "removed"} {
		m := ShopEventMessage(ev(k))
		if seen[m] {
			t.Fatalf("%q 與其他類型的訊息相同，分不出差別", k)
		}
		seen[m] = true
	}
}

func TestShopEventPriceDirection(t *testing.T) {
	down := ev("price")
	up := ev("price")
	up.OldPrice, up.NewPrice = 1200, 1500
	if !strings.Contains(ShopEventMessage(down), "↓") || !strings.Contains(ShopEventMessage(up), "↑") {
		t.Error("漲跌方向應該看得出來")
	}
}

func TestShopEventFallbacks(t *testing.T) {
	e := ShopEvent{Kind: "new", ShopName: "  ", ItemName: "", NewPrice: 100, NewQty: 1, ShopURL: url}
	if got := ShopEventMessage(e); strings.Contains(got, "　　") || strings.Contains(got, "\n\n") {
		t.Errorf("缺名稱時不該留下連續空白: %q", got)
	}
	if got := ShopEventEmbed(e); got.Title == "" {
		t.Error("卡片標題不該是空的")
	}
}

// 做卡片的理由就是認商品靠圖最快 —— 圖掉了這個功能就沒意義了
func TestShopEventEmbedCarriesImage(t *testing.T) {
	for _, k := range []string{"new", "relist", "restock", "price"} {
		em := ShopEventEmbed(ev(k))
		if em.Image == nil || em.Image.URL != img {
			t.Errorf("%s 還買得到，應該用大圖: %+v", k, em.Image)
		}
		if em.Thumbnail != nil {
			t.Errorf("%s 不該同時給小圖", k)
		}
	}
	// 已經沒了的只是動態，用小圖就好，不值得佔滿整個頻道
	for _, k := range []string{"soldout", "removed"} {
		em := ShopEventEmbed(ev(k))
		if em.Thumbnail == nil || em.Thumbnail.URL != img {
			t.Errorf("%s 應該用小圖: %+v", k, em.Thumbnail)
		}
		if em.Image != nil {
			t.Errorf("%s 不該用大圖", k)
		}
	}
}

func TestShopEventEmbedWithoutImage(t *testing.T) {
	e := ev("new")
	e.ImageURL = ""
	em := ShopEventEmbed(e)
	if em.Image != nil || em.Thumbnail != nil {
		t.Error("沒有圖片時不該塞空網址進去 —— Discord 會整張卡片退回")
	}
}

// 商品沒有各自的網址，卡片標題只能指到賣場頁
func TestShopEventEmbedLinksToShop(t *testing.T) {
	if got := ShopEventEmbed(ev("new")).URL; got != url {
		t.Errorf("卡片網址 = %q, want %q", got, url)
	}
}

// 賣貨便常常只有 1 件庫存，「剩 1 件」決定的是要不要現在就下單
func TestShopEventEmbedShowsRemaining(t *testing.T) {
	if got := ShopEventEmbed(ev("new")).Description; !strings.Contains(got, "剩 1 件") {
		t.Errorf("描述應寫出剩餘量: %q", got)
	}
	// 完售與下架沒有剩餘量可寫，改寫原價
	if got := ShopEventEmbed(ev("soldout")).Description; strings.Contains(got, "剩") {
		t.Errorf("完售不該寫剩餘量: %q", got)
	}
	if got := ShopEventEmbed(ev("removed")).Description; !strings.Contains(got, "1500") {
		t.Errorf("消失時應寫出最後的價格: %q", got)
	}
}

func TestShopEventEmbedColorsDiffer(t *testing.T) {
	seen := map[int]string{}
	for _, k := range []string{"new", "relist", "price", "restock", "soldout", "removed"} {
		c := ShopEventEmbed(ev(k)).Color
		if prev, dup := seen[c]; dup {
			t.Errorf("%s 與 %s 的顏色相同 (%#x)", k, prev, c)
		}
		seen[c] = k
	}
}

// Discord 會把帶空字串網址的 embed 整張退回，所以序列化時那些欄位必須消失
func TestShopEventEmbedOmitsEmptyFields(t *testing.T) {
	e := ev("new")
	e.ImageURL = ""
	b, err := json.Marshal(ShopEventEmbed(e))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"image", "thumbnail"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("序列化後不該出現 %q: %s", k, b)
		}
	}
}

// 整件賣完被藏起來、補貨後又冒出來的舊商品，不能報成「新上架」——
// 點進去會發現它排在賣場很後面、建檔時間是幾個月前，看起來像壞掉了
func TestRelistReadsAsRelistNotNew(t *testing.T) {
	e := ev("relist")
	msg := ShopEventMessage(e)
	if !strings.Contains(msg, "重新上架") {
		t.Errorf("應寫「重新上架」: %q", msg)
	}
	if msg == ShopEventMessage(ev("new")) {
		t.Errorf("不該跟新上架長得一樣: %q", msg)
	}
	// 還買得到，所以跟新上架一樣用大圖
	if em := ShopEventEmbed(e); em.Image == nil {
		t.Error("重新上架還買得到，應該用大圖")
	}
}

func TestShopEventSummonableOnlyWhenBuyable(t *testing.T) {
	for k, want := range map[string]bool{"new": true, "relist": true, "restock": true, "soldout": false, "removed": false} {
		if got := ev(k).Summonable(); got != want {
			t.Errorf("%s: Summonable = %v, want %v", k, got, want)
		}
	}
	down, up := ev("price"), ev("price")
	down.OldPrice, down.NewPrice = 1500, 1200
	up.OldPrice, up.NewPrice = 1200, 1500
	if !down.Summonable() || up.Summonable() {
		t.Error("降價要叫、漲價不叫")
	}
}

func TestShopEventMessageMarksUsed(t *testing.T) {
	e := ev("new")
	e.Used = true
	first := strings.Split(ShopEventMessage(e), "\n")[0]
	if !strings.Contains(first, "（二手）") {
		t.Errorf("二手要在第一行標出來：%q", first)
	}
	if strings.Contains(ShopEventMessage(ev("new")), "二手") {
		t.Error("新品不該標")
	}
}
