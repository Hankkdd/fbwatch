// Package myship 解析 7-11 賣貨便的賣場頁。
//
// 跟 FB 那條路完全不同，而且簡單得多：伺服器端渲染、不需要登入、
// 不需要瀏覽器，純 HTTP GET 就拿得到。所以這裡沒有 session、
// 沒有熔斷、也沒有帳號風險要管。
//
// 商品資料全在 data-product 屬性裡的一包 JSON，連上架時間都有，
// 不必像 FB 那樣另外補抓詳情頁。
package myship

import (
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Spec struct {
	ID    string
	Name  string
	Price int

	// Stock 是賣家上架時填的數量，Reserved 是已經被下單的，Remaining 才是還買得到的。
	//
	// 這三個必須分開：有人買走時 Stock 動都不動，只有 Reserved 往上、Remaining 往下。
	// 2026-10-08 實測 139 件商品裡有 42 件 Remaining 已經是 0，
	// 但 Stock 全都還是原值 —— 只看 Stock 會把整個賣場都當成有貨。
	Stock     int
	Reserved  int
	Remaining int
}

type Item struct {
	ShopID      string
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	// Status 是商品保存情況，不是上下架狀態：「1」新品、「2」二手。
	// 2026-11-09 對照賣場彈窗確認（status 2 的那件寫「二手」、規格「已組單人」，而且還有庫存）。
	// 以前誤以為 2 是下架或售罄；有沒有貨要看 Remaining。
	Status string
	Image  string
	Specs  []Spec
}

// MinPrice 回傳還買得到的最低規格價，-1 表示沒有任何規格買得到。
//
// 必須排除賣完的規格。一件商品常常同時掛著主體與配件 ——
// 「處決者型反重力坦克」有完整盒裝 $2700 與水貼 $30 兩種規格，
// 水貼賣掉之後若還拿 $30 當售價，通知會變成「新上架 $30」，
// 跟實際買得到的價格差了快一百倍。
// Condition 把 Status 換成人看得懂的保存情況，認不得的回傳空字串。
func (i Item) Condition() string { return ConditionLabel(i.Status) }

// ConditionLabel 是 Condition 的獨立版本，給只拿得到 status 字串的地方用（例如資料庫）。
func ConditionLabel(status string) string {
	switch status {
	case "1":
		return "新品"
	case "2":
		return "二手"
	}
	return ""
}

func (i Item) MinPrice() int {
	min := -1
	for _, s := range i.Specs {
		if s.Price > 0 && s.Remaining > 0 && (min < 0 || s.Price < min) {
			min = s.Price
		}
	}
	return min
}

// LastPrice 回傳所有規格的最低價，不管還買不買得到。
// 整件完售或下架時沒有「買得到的價格」，但通知裡還是要有個數字可寫。
func (i Item) LastPrice() int {
	min := -1
	for _, s := range i.Specs {
		if s.Price > 0 && (min < 0 || s.Price < min) {
			min = s.Price
		}
	}
	return min
}

// Price 是寫進資料庫的售價：買得到的最低價優先，整件賣完才退回最後的價格。
func (i Item) Price() int {
	if p := i.MinPrice(); p > 0 {
		return p
	}
	return i.LastPrice()
}

// Remaining 回傳所有規格還買得到的數量總和。0 代表完售。
func (i Item) Remaining() int {
	n := 0
	for _, s := range i.Specs {
		n += s.Remaining
	}
	return n
}

// Stock 回傳原始上架量總和。只用來跟 Remaining 對照，不拿來判斷完售。
func (i Item) Stock() int {
	n := 0
	for _, s := range i.Specs {
		n += s.Stock
	}
	return n
}

var reDataProduct = regexp.MustCompile(`data-product="([^"]+)"`)

// 賣貨便的 JSON 欄位名稱
type rawProduct struct {
	ShopID      string `json:"Cgdd_Cgdmid"`
	ID          string `json:"Cgdd_Id"`
	Name        string `json:"Cgdd_Product_Name"`
	Description string `json:"Cgdd_Product_Description"`
	Status      string `json:"Cgdd_Product_Status"`
	Created     string `json:"Cgdd_Create_Datetime"`
	Spec        []struct {
		ID        string       `json:"Cgds_Id"`
		Name      string       `json:"Cgds_Spec"`
		Price     json.Number  `json:"Cgds_Price"`
		Stock     json.Number  `json:"Cgds_Inventory"`
		Reserved  json.Number  `json:"Cgds_Qty_Reg"`
		Remaining *json.Number `json:"Inventory"`
	} `json:"Spec"`
	Images []struct {
		Path     string      `json:"Cgim_Image_Path"`
		Ordering json.Number `json:"Cgim_Ordering"`
	} `json:"Images"`
}

// Parse 取出賣場頁上的所有商品。
//
// 同一件商品在頁面上會出現兩次（清單一次、彈窗一次），必須用 id 去重 ——
// 實測 143 件商品對應 286 個 data-product。
func Parse(doc string) []Item {
	seen := map[string]bool{}
	var out []Item

	for _, m := range reDataProduct.FindAllStringSubmatch(doc, -1) {
		var p rawProduct
		if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &p); err != nil {
			continue
		}
		if p.ID == "" || seen[p.ID] {
			continue
		}
		seen[p.ID] = true

		it := Item{
			ShopID:      p.ShopID,
			ID:          p.ID,
			Name:        strings.TrimSpace(p.Name),
			Description: strings.TrimSpace(p.Description),
			Status:      p.Status,
			CreatedAt:   parseCreated(p.Created),
		}
		for _, s := range p.Spec {
			sp := Spec{
				ID: s.ID, Name: strings.TrimSpace(s.Name),
				Price: toInt(s.Price), Stock: toInt(s.Stock), Reserved: toInt(s.Reserved),
			}
			// Inventory 欄位缺席時用減的補 —— 寧可算出來，也不要默默回報成有貨
			if s.Remaining != nil {
				sp.Remaining = toInt(*s.Remaining)
			} else {
				sp.Remaining = sp.Stock - sp.Reserved
			}
			if sp.Remaining < 0 {
				sp.Remaining = 0
			}
			it.Specs = append(it.Specs, sp)
		}
		if len(p.Images) > 0 {
			it.Image = p.Images[0].Path
		}
		out = append(out, it)
	}
	return out
}

// 賣貨便的時間沒有帶時區，實測是台灣當地時間
func parseCreated(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

func toInt(n json.Number) int {
	v, err := strconv.Atoi(n.String())
	if err != nil {
		return 0
	}
	return v
}

// ShopURL 回傳賣場頁網址。
func ShopURL(shopID string) string {
	return "https://myship.7-11.com.tw/general/detail/" + shopID
}

// ItemURL 回傳會直接捲到該商品的網址。
//
// 賣貨便沒有商品頁：詳情是彈窗，搜尋是 POST 帶 CSRF token，
// 平台自己的「複製網址」也只複製賣場頁。但每張商品卡片的 <a>
// 都帶著 id="<商品id>"，所以用 # 片段就能讓瀏覽器自己捲過去 ——
// 不需要平台配合，是純 HTML 的錨點。
//
// 2026-10-08 用獨立的 headless Chrome 實測：頁高 47289px，
// 開這個網址會停在 44281px，目標商品正好在視窗頂端。
// 商品被下架時片段找不到，瀏覽器就停在賣場頁頂端，不會壞掉。
func ItemURL(shopID, itemID string) string {
	if itemID == "" {
		return ShopURL(shopID)
	}
	return ShopURL(shopID) + "#" + itemID
}

// ImageURL 回傳商品圖片網址。
func ImageURL(shopID, path string) string {
	if path == "" {
		return ""
	}
	return "https://myship.7-11.com.tw/i/cgdm/" + shopID + "/" + path
}
