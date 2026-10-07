package myship

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ShopURL(shopID), nil)
	if err != nil {
		return nil, err
	}
	// 帶一個一般瀏覽器的 UA：有些站對空 UA 會回不同內容
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9")

	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("賣場頁回應 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil {
		return nil, err
	}

	items := Parse(string(body))
	if len(items) == 0 {
		// 頁面拿得到但解析不出商品，多半是版型變了 —— 要當成錯誤，
		// 否則會靜默地把整個賣場當成「全部下架」
		return nil, fmt.Errorf("頁面解析不出任何商品（%d 位元組），版型可能已變更", len(body))
	}
	return items, nil
}
