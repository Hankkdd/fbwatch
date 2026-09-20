package parse

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Detail 是從 listing 詳情頁的內嵌 JSON 取得的資料。
//
// 詳情頁的 DOM 描述同樣被伺服器端截斷（也有「查看更多」），
// 但內嵌 JSON 裡是完整的，所以不需要任何點擊或互動。
type Detail struct {
	Description string
	CreatedAt   time.Time // 精確的 unix 時間，優於 feed 上的相對時間推算
	Location    string
	Price       int
	Currency    string
}

// 貼文頁的內文所在。整串比對，見 ListingPage 裡的說明。
const msgNeedle = `"message":{"text":`

type jsonText struct {
	Text string `json:"text"`
}

type jsonPrice struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// ListingPage 從詳情頁的 HTML 取出結構化資料。
//
// 第二個回傳值表示是否認得這是一個商品頁。只有照片、沒有文字描述的貼文
// 仍算成功 —— 它的 creation_time 一樣有價值，不該讓它反覆重試。
func ListingPage(doc string) (Detail, bool) {
	var d Detail

	// 商品頁用 redacted_description；一般貼文頁沒有這個欄位，內文在 message.text。
	var desc jsonText
	if decodeFieldAfter(doc, "redacted_description", &desc) {
		d.Description = strings.TrimSpace(desc.Text)
	}
	if d.Description == "" {
		// 必須比對 `"message":{"text":` 整串。單看 `"message":` 在貼文頁
		// 會命中一堆別的東西（多半是 null），結果解出空字串。
		if i := strings.Index(doc, msgNeedle); i >= 0 {
			var msg jsonText
			if err := json.NewDecoder(
				strings.NewReader(doc[i+len(`"message":`):]),
			).Decode(&msg); err == nil {
				d.Description = strings.TrimSpace(msg.Text)
			}
		}
	}

	var ct int64
	if decodeFieldAfter(doc, "creation_time", &ct) && ct > 0 {
		d.CreatedAt = time.Unix(ct, 0)
	}

	var loc jsonText
	if decodeFieldAfter(doc, "location_text", &loc) {
		d.Location = strings.TrimSpace(loc.Text)
	}

	var price jsonPrice
	d.Price = -1
	if decodeFieldAfter(doc, "listing_price", &price) {
		if v, err := strconv.Atoi(price.Amount); err == nil {
			d.Price = v
		}
		d.Currency = price.Currency
	}

	return d, d.Description != "" || !d.CreatedAt.IsZero()
}

// decodeFieldAfter 找到 `"key":` 之後用 json.Decoder 解析接下來那個值。
//
// 不用正則抓字串內容：JSON 裡的中文是 \uXXXX 跳脫、引號是 \"，
// 正則要正確處理這些跳脫很容易出錯，交給 decoder 才可靠。
func decodeFieldAfter(s, key string, v any) bool {
	needle := `"` + key + `":`
	i := strings.Index(s, needle)
	if i < 0 {
		return false
	}
	dec := json.NewDecoder(strings.NewReader(s[i+len(needle):]))
	return dec.Decode(v) == nil
}
