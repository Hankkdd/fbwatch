package notify

import "strings"

// Post 是一則 FB 貼文要送出的素材。
type Post struct {
	Highlight string // 關鍵字命中的那幾行，可為空
	Title     string // 商品標題，可為空
	Body      string
	Permalink string
}

// PostMessage 排出一則貼文的通知內容：
//
//	**[限量品出現了]**
//	**售 限量版海報**
//	1500 半塗 ...
//	<https://facebook.com/...>
//
// 網址放最後，因為手機推播只看得到開頭幾十個字 ——
// FB 的 permalink 又長又沒有可讀資訊，放在第一行等於整條推播被它吃掉，
// 得點開才知道在賣什麼。內文開頭通常就是品項與價格，那才是該出現在
// 推播列上的東西。
//
// 網址仍然用 <> 包住，讓 Discord 不要展開預覽卡片。
func PostMessage(p Post) string {
	var lines []string
	if h := strings.TrimSpace(p.Highlight); h != "" {
		lines = append(lines, h)
	}
	// 標題放在內文前面：很多內文只寫「售如標題」，品名只在標題裡。
	// 內文已經包含標題的就不重複，不然通知列上同一句會出現兩次。
	if t := strings.TrimSpace(p.Title); t != "" && !strings.Contains(p.Body, t) {
		lines = append(lines, "**"+t+"**")
	}
	if b := strings.TrimRight(p.Body, "\n"); strings.TrimSpace(b) != "" {
		lines = append(lines, b)
	}
	if p.Permalink != "" {
		lines = append(lines, "<"+p.Permalink+">")
	}
	return strings.Join(lines, "\n")
}

// WithMention 在訊息第一行尾巴加上 <@userID>。
//
// 放第一行而不是最後：長貼文會被 Split 切成好幾則，mention 落在後面那幾則的話，
// 被叫出來的 bot 拿到的只是一段沒頭沒尾的內文。第一則一定帶著提示行和標題。
func WithMention(msg, userID string) string {
	if userID == "" {
		return msg
	}
	first, rest, found := strings.Cut(msg, "\n")
	first += " <@" + userID + ">"
	if !found {
		return first
	}
	return first + "\n" + rest
}
