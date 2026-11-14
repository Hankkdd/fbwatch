package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// minKeyLen 是參與去重的最短長度（正規化後的字元數）。
//
// 「+1」「收」「有興趣」這類極短內容在不同貼文之間太容易撞在一起。
// 去重另外要求同一個賣家，所以門檻不必很高，但還是要有 ——
// 同一個賣家確實會重複貼「完售」「已售出」這種短句。
const minKeyLen = 10

// ContentKey 是「同樣的內容」的識別鍵。
//
// 以內文為準，標題只在內文短到算不出鍵的時候才借過來。商品卡片的內文
// 常常只是運送資訊 ——「店到店運費60」只有 7 個字 —— 實測 1134 則有內文的
// 貼文裡有 28 則因此完全不參與比對。
//
// 不無條件併入標題，是量過之後的決定。同賣家、內文逐字相同但標題不同的有
// 14 組 / 31 則，翻開來看幾乎都是同一件東西被重貼：標題打錯一個字母、
// 補上「已售出」、同樣的意思換個說法。併入標題會讓這些全變成不同的鍵，
// 等於少擋 17 則。而「標題不同、內文是共用的運送說明、商品其實不一樣」
// 這個情境，在 1206 則裡一次都沒出現。
//
// 只正規化空白，不動標點與 emoji：差一個 🥺 就算不同內容是刻意的，
// 寧可少抓一組重複，也不要把不同的貼文併掉。
func ContentKey(title, body string) string {
	t := strings.Join(strings.Fields(body), " ")
	if len([]rune(t)) < minKeyLen {
		// 借標題時不加分隔符號：同樣的字寫在標題還是內文本來就是同一件事。
		t = strings.Join(strings.Fields(title+" "+body), " ")
	}
	if len([]rune(t)) < minKeyLen {
		return ""
	}
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:16])
}
