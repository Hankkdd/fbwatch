package myship

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrShopOffline 是賣家把整個賣場關掉。
//
// 這跟「解析不出商品」要分開：版型變了是我們要修的程式問題，
// 賣場關了是對方的決定，重試一萬次也不會好。2026-10-27 發現
// 一個賣場從 10-17 起就是這個狀態，每分鐘失敗一次連續十天，
// 完全沒有告警 —— 錯誤被當成雜訊，就等於沒有在監控。
var ErrShopOffline = errors.New("賣場已由賣家下架")

// ErrSiteUnavailable 是賣貨便站台當下沒反應：連不上、超時、或 5xx。
//
// 要跟「解析不出商品」分開，因為處置相反：站台沒反應會自己好，等就是了；
// 版型變了則是一分鐘都不該等，要去改程式。2026-11-02 00:00 前後賣貨便
// 回了一串 503 與超時（大概是維護），告警卻寫「可能是版型變了」——
// 錯誤內容在第二行，但標題把人指向完全錯的方向。
//
// 用 sentinel 而不是比對錯誤字串：字串會跟著文案改，errors.Is 不會。
var ErrSiteUnavailable = errors.New("賣貨便站台沒有回應")

// 賣場關掉時頁面照樣回 HTTP 200，只有內文會說。訊息代碼比中文穩定，
// 但兩個都認：文案可能改，代碼可能換。
var offlineMarkers = []string{"0102M1", "此賣場已由賣家自行下架"}

// isOffline 判斷這頁是不是「賣場已下架」。
func isOffline(page string) bool {
	for _, m := range offlineMarkers {
		if strings.Contains(page, m) {
			return true
		}
	}
	return false
}

// maxPage 擋掉異常大的回應。實測一個 143 件商品的賣場約 1.2MB。
const maxPage = 24 << 20

type Fetcher struct{ HTTP *http.Client }

func NewFetcher() *Fetcher {
	return &Fetcher{HTTP: &http.Client{Timeout: 45 * time.Second}}
}

// Fetch 取得賣場頁並解析出商品。
//
// 純 HTTP、不需要登入也不需要瀏覽器 —— 這是賣貨便相對 FB 最大的差別，
// 所以這裡沒有 session 要保管，也沒有帳號可以被封。
func (f *Fetcher) Fetch(ctx context.Context, shopID string) ([]Item, error) {
	return f.fetchURL(ctx, ShopURL(shopID))
}

// fetchURL 是 Fetch 的本體，拆出來只為了測試能指向 httptest 的假站台。
// 錯誤分類（站台沒反應 vs 版型變了）是這裡最該被測到的邏輯。
func (f *Fetcher) fetchURL(ctx context.Context, url string) ([]Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// 帶一個一般瀏覽器的 UA：有些站對空 UA 會回不同內容
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9")

	resp, err := f.HTTP.Do(req)
	if err != nil {
		// 連不上、DNS、超時都走這裡 —— 都是對方或網路的事，不是版型
		return nil, fmt.Errorf("%w: %w", ErrSiteUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 500:
		// 5xx 是對方自己壞了或在維護
		return nil, fmt.Errorf("%w: HTTP %d", ErrSiteUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		// 4xx 才是真的該看一眼：下架的賣場照樣回 200，所以 404 代表網址規則變了
		return nil, fmt.Errorf("賣場頁回應 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil {
		// 讀到一半斷線，同樣是連線問題
		return nil, fmt.Errorf("%w: %w", ErrSiteUnavailable, err)
	}

	page := string(body)
	items := Parse(page)
	if len(items) == 0 {
		if isOffline(page) {
			return nil, ErrShopOffline
		}
		// 頁面拿得到但解析不出商品，多半是版型變了 —— 要當成錯誤，
		// 否則會靜默地把整個賣場當成「全部下架」
		return nil, fmt.Errorf("頁面解析不出任何商品（%d 位元組），版型可能已變更", len(body))
	}
	return items, nil
}
