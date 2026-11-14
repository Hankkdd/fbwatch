package notify

import "strings"

// MaxMessage 是 Discord 單則訊息的字元上限。
// 留一點餘裕，不要貼著 2000 送。
const MaxMessage = 1900

// Split 把長文切成多則訊息，內容一字不改。
//
// 目錄型貼文動輒數千字（實測有 16 項商品的貼文），超過上限時
// 切段而非截斷 —— 被截掉的往往正是後半段的品項與價格。
//
// 切點優先順序：段落 > 換行 > 硬切。盡量不要切在句子中間。
func Split(s string, limit int) []string {
	if limit <= 0 {
		limit = MaxMessage
	}
	r := []rune(s)
	if len(r) <= limit {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []string{s}
	}

	var out []string
	for len(r) > limit {
		cut := breakPoint(r, limit)
		out = append(out, strings.TrimRight(string(r[:cut]), "\n"))
		// 切點本身是換行的話就跳過，避免下一段以空行開頭
		for cut < len(r) && r[cut] == '\n' {
			cut++
		}
		r = r[cut:]
	}
	if rest := strings.TrimSpace(string(r)); rest != "" {
		out = append(out, string(r))
	}
	return out
}

// breakPoint 在 limit 之內找最靠後的自然切點。
func breakPoint(r []rune, limit int) int {
	// 段落分隔優先
	for i := limit - 1; i > limit/2; i-- {
		if r[i] == '\n' && i > 0 && r[i-1] == '\n' {
			return i
		}
	}
	for i := limit - 1; i > limit/2; i-- {
		if r[i] == '\n' {
			return i
		}
	}
	return limit
}
