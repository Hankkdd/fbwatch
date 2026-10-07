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
	ID        string
	Name      string
	Price     int
	Inventory int
}

type Item struct {
	ShopID      string
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	Status      string // "1" 正常、"2" 實測為下架或售罄
	Image       string
	Specs       []Spec
}

// MinPrice 回傳最低規格價，-1 表示沒有規格。
// 一件商品可能有多種規格（不同盒況），比價時以最低價為準。
func (i Item) MinPrice() int {
	min := -1
	for _, s := range i.Specs {
		if s.Price > 0 && (min < 0 || s.Price < min) {
			min = s.Price
		}
	}
	return min
}

// Inventory 回傳所有規格的庫存總和。0 代表完售。
func (i Item) Inventory() int {
	n := 0
	for _, s := range i.Specs {
		n += s.Inventory
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
		ID        string          `json:"Cgds_Id"`
		Name      string          `json:"Cgds_Spec"`
		Price     json.Number     `json:"Cgds_Price"`
		Inventory json.Number     `json:"Cgds_Inventory"`
		Extra     json.RawMessage `json:"-"`
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
			it.Specs = append(it.Specs, Spec{
				ID: s.ID, Name: strings.TrimSpace(s.Name),
				Price: toInt(s.Price), Inventory: toInt(s.Inventory),
			})
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

// ShopURL 回傳賣場頁網址。商品沒有各自的網址 ——
// 整頁用彈窗呈現，所以通知只能連到賣場。
func ShopURL(shopID string) string {
	return "https://myship.7-11.com.tw/general/detail/" + shopID
}

// ImageURL 回傳商品圖片網址。
func ImageURL(shopID, path string) string {
	if path == "" {
		return ""
	}
	return "https://myship.7-11.com.tw/i/cgdm/" + shopID + "/" + path
}
