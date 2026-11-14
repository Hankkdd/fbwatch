package notify

import "fmt"

// Router 依名稱挑 webhook，讓不同社團通知到不同聊天室。
//
// 名稱是環境變數名，不是 webhook 本身 —— 憑證留在 .env，資料庫只存路由。
// 這樣 pg_dump 出來的檔案不會帶著一把可以對那個頻道發文的鑰匙。
type Router struct {
	def    *Discord
	lookup func(string) string
	cache  map[string]*Discord
}

// NewRouter 建立路由。def 是沒有指定時用的預設管道，
// lookup 通常傳 os.Getenv（拆出來是為了測試）。
func NewRouter(def *Discord, lookup func(string) string) *Router {
	return &Router{def: def, lookup: lookup, cache: map[string]*Discord{}}
}

// Default 回傳預設管道。告警與賣貨便變動都走這個 ——
// 只有社團貼文會分流，系統訊息永遠送到主頻道。
func (r *Router) Default() *Discord { return r.def }

// For 回傳某個名稱對應的管道。
//
// 名稱為空表示用預設的。名稱有指定但環境變數是空的時候**不會**退回預設，
// 而是回報錯誤：這種情況下退回預設等於把貼文送到錯誤的聊天室，
// 而要分流的理由本來就是「不要送到那裡」。少送一則可以補，送錯地方收不回來。
func (r *Router) For(envName string) (*Discord, error) {
	if envName == "" {
		return r.def, nil
	}
	if dc, ok := r.cache[envName]; ok {
		return dc, nil
	}
	url := r.lookup(envName)
	if url == "" {
		return nil, fmt.Errorf("環境變數 %s 沒有設定或是空的，不送出以免送錯聊天室", envName)
	}
	dc := NewDiscord(url)
	r.cache[envName] = dc
	return dc, nil
}
