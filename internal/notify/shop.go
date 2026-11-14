package notify

import (
	"fmt"
	"strings"
)

// ShopEvent 是一則賣貨便的變動。欄位比 store 那邊少 ——
// 通知只需要這些，多帶會讓這個套件黏上資料庫的形狀。
type ShopEvent struct {
	Kind     string // new | relist | price | restock | soldout | removed
	ShopName string
	ItemName string
	OldPrice int
	NewPrice int
	OldQty   int
	NewQty   int
	ShopURL  string
	ImageURL string
	Used     bool // 二手。新品是常態，不另外標
}

// 卡片顏色按「要不要馬上看」分層：能買的亮、已經沒了的灰。
const (
	colorNew      = 0x2ECC71 // 綠：新上架
	colorRelist   = 0x1ABC9C // 青：舊商品補貨後重新出現
	colorRestock  = 0x3498DB // 藍：補貨
	colorPrice    = 0xE67E22 // 橘：改價
	colorSoldout  = 0x95A5A6 // 灰：完售
	colorRemoved  = 0x7F8C8D // 深灰：賣家下架
	colorFallback = 0x546E7A
)

func (e ShopEvent) shop() string {
	if strings.TrimSpace(e.ShopName) == "" {
		return "賣場"
	}
	return e.ShopName
}

func (e ShopEvent) item() string {
	if strings.TrimSpace(e.ItemName) == "" {
		return "（無名稱）"
	}
	return e.ItemName
}

// tail 是「發生了什麼」那一小段。少了它，新上架與完售會長得一模一樣。
func (e ShopEvent) tail() string {
	switch e.Kind {
	case "new":
		return fmt.Sprintf("新上架 $%d", e.NewPrice)
	case "relist":
		return fmt.Sprintf("重新上架 $%d", e.NewPrice)
	case "price":
		arrow := "↓"
		if e.NewPrice > e.OldPrice {
			arrow = "↑"
		}
		return fmt.Sprintf("$%d %s $%d", e.OldPrice, arrow, e.NewPrice)
	case "restock":
		return fmt.Sprintf("補貨 $%d", e.NewPrice)
	case "soldout":
		return "完售"
	case "removed":
		return "已從賣場消失"
	default:
		return e.Kind
	}
}

// ShopEventMessage 是卡片上方那段推播文字，分兩行：
//
//	稱呼　發生什麼
//	商品名稱
//
//	<商品網址>
//
// 三段擠成一行時商品名稱夾在中間，手機寬度下會亂折行。
// 稱呼與「發生什麼」都短，放同一行；長的商品名稱自己獨佔一行。
//
// 網址放最後一行，用 <> 包住讓 Discord 不要另外長預覽。卡片標題上已經有連結，
// 但比價 bot 讀被回覆的訊息時只看得到 content，看不到卡片 ——
// 沒有這行它就不知道是哪個賣場、哪一件。網址在最後，不影響推播列上的前兩行。
func ShopEventMessage(e ShopEvent) string {
	tail := e.tail()
	if e.Used {
		tail += "（二手）"
	}
	msg := fmt.Sprintf("%s　%s\n%s", e.shop(), tail, e.item())
	if e.ShopURL != "" {
		msg += "\n<" + e.ShopURL + ">"
	}
	return msg
}

// ShopEventEmbed 把變動排成卡片。
//
// 做這個的理由只有一個：賣貨便的商品沒有各自的網址。平台的搜尋是
// POST 帶 CSRF token，做不出可點的連結；它自己的「複製網址」按鈕
// 複製的也是賣場頁。所以連結只能指到賣場，光看文字很難認出是哪一件。
// 但圖片有各自的網址而且公開可讀 —— 認商品靠圖最快。
//
// 還買得到的用大圖（看得清楚才決定要不要搶），已經沒了的用小圖
// （那只是「有人買走了」的動態，不值得佔滿整個頻道）。
func ShopEventEmbed(e ShopEvent) Embed {
	em := Embed{
		Title:       e.item(),
		URL:         e.ShopURL,
		Description: e.description(),
		Color:       e.color(),
		Author:      &EmbedAuthor{Name: e.shop()},
	}
	if e.ImageURL != "" {
		if e.actionable() {
			em.Image = &EmbedImage{URL: e.ImageURL}
		} else {
			em.Thumbnail = &EmbedImage{URL: e.ImageURL}
		}
	}
	return em
}

func (e ShopEvent) actionable() bool {
	switch e.Kind {
	case "new", "relist", "restock", "price":
		return true
	}
	return false
}

func (e ShopEvent) color() int {
	switch e.Kind {
	case "new":
		return colorNew
	case "relist":
		return colorRelist
	case "restock":
		return colorRestock
	case "price":
		return colorPrice
	case "soldout":
		return colorSoldout
	case "removed":
		return colorRemoved
	}
	return colorFallback
}

// description 是卡片裡那一行：發生什麼，加上還剩幾件。
//
// 剩餘量要寫出來：賣貨便一件商品常常只有 1 件庫存，
// 「剩 1 件」跟「剩 8 件」決定的是要不要現在就下單。
func (e ShopEvent) description() string {
	parts := []string{e.tail()}
	switch e.Kind {
	case "soldout":
		if e.NewPrice > 0 {
			parts = []string{fmt.Sprintf("完售　原價 $%d", e.NewPrice)}
		}
	case "removed":
		// 不寫「賣家下架」—— 整件賣完也會被平台藏起來，之後補貨還會回來
		if e.OldPrice > 0 {
			parts = []string{fmt.Sprintf("已從賣場消失　原價 $%d", e.OldPrice)}
		}
	default:
		if e.NewQty > 0 {
			parts = append(parts, fmt.Sprintf("剩 %d 件", e.NewQty))
		}
	}
	return strings.Join(parts, "　")
}

// Summonable 回報這個變動值不值得叫比價 bot 來比價：只有現在買得到、而且沒有變貴的。
// 完售、下架、漲價都是動態，不是購買機會。
func (e ShopEvent) Summonable() bool {
	switch e.Kind {
	case "new", "relist", "restock":
		return true
	case "price":
		return e.NewPrice < e.OldPrice
	}
	return false
}
