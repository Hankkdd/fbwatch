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
	Photos      []Photo
}

// Photo 是商品照片。
//
// Caption 是 FB 自動生成的圖片描述，含影像辨識出的文字（常包含產品名稱），
// 對之後的關鍵字搜尋是免費的索引來源。
type Photo struct {
	ID      string
	URL     string
	Width   int
	Height  int
	Caption string
}

type jsonPhoto struct {
	ID      string `json:"id"`
	Caption string `json:"accessibility_caption"`
	Image   struct {
		URI    string `json:"uri"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"image"`
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

	d.Photos = extractPhotos(doc)
	return d, d.Description != "" || !d.CreatedAt.IsZero()
}

// minPhotoEdge 濾掉大頭貼、圖示這類小圖。
// 商品照片遠大於此；實測誤抓過 80x80 的大頭貼。
const minPhotoEdge = 200

// extractPhotos 同時支援兩種頁型：
//   - 商品頁：listing_photos[].image
//   - 貼文頁：media.photo_image
//
// 只從這兩個已知欄位取，不是掃整份文件找 scontent 網址 ——
// 那會連大頭貼、表情符號、介面圖示一起抓進來。
func extractPhotos(doc string) []Photo {
	var out []Photo
	seen := map[string]bool{}

	add := func(p Photo) {
		if p.URL == "" || p.Width < minPhotoEdge || p.Height < minPhotoEdge {
			return
		}
		// 同一張圖會在不同渲染脈絡重複出現，網址的 query 帶不同簽章，
		// 用路徑部分去重才有效
		key := p.URL
		if i := strings.IndexByte(key, '?'); i >= 0 {
			key = key[:i]
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, p)
	}

	var listing []jsonPhoto
	if decodeFieldAfter(doc, "listing_photos", &listing) {
		for _, p := range listing {
			add(Photo{ID: p.ID, URL: p.Image.URI,
				Width: p.Image.Width, Height: p.Image.Height,
				Caption: strings.TrimSpace(p.Caption)})
		}
	}

	// 多圖貼文的其餘照片在 all_subattachments.nodes[].media.image。
	// 貼文頁的初始 HTML 只渲染封面，但這個欄位帶了一整批 ——
	// 實測一則 42 張圖的貼文，photo_image 只有 1 張，這裡有其餘的。
	// 先取這裡，讓順序以附件順序為準。
	for _, sub := range decodeAll[jsonSubattachments](doc, `"all_subattachments":`) {
		for _, n := range sub.Nodes {
			add(Photo{URL: n.Media.Image.URI,
				Width: n.Media.Image.Width, Height: n.Media.Image.Height})
		}
	}

	for _, img := range decodeAll[jsonImage](doc, `"photo_image":`) {
		add(Photo{URL: img.URI, Width: img.Width, Height: img.Height})
	}

	return out
}

type jsonImage struct {
	URI    string `json:"uri"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type jsonSubattachments struct {
	Count int `json:"count"`
	Nodes []struct {
		Media struct {
			Image jsonImage `json:"image"`
		} `json:"media"`
	} `json:"nodes"`
}

// decodeAll 找出所有 needle 之後的 JSON 值並解碼。
// 同一份文件裡這些欄位會出現多次（不同渲染脈絡），全部都要看。
func decodeAll[T any](doc, needle string) []T {
	var out []T
	for i := 0; ; {
		j := strings.Index(doc[i:], needle)
		if j < 0 {
			return out
		}
		i += j + len(needle)
		var v T
		if err := json.NewDecoder(strings.NewReader(doc[i:])).Decode(&v); err == nil {
			out = append(out, v)
		}
	}
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
