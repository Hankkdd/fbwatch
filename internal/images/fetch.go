// Package images 下載商品照片。
//
// 圖片走 scontent.*.fbcdn.net，**不需要登入態也不需要瀏覽器** ——
// 實測純 HTTP GET 即可（HTTP 200，無 cookie）。所以下載圖片對帳號風險
// 幾乎為零：不同主機、不帶 session、不增加 facebook.com 的請求。
//
// 但網址帶簽章會過期，必須在抓到詳情頁的當下就下載，不能只存網址。
package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultMaxBytes 擋掉異常大的回應。商品照片通常在 1MB 以內。
	DefaultMaxBytes = 12 << 20
	defaultTimeout  = 30 * time.Second
)

type Fetcher struct {
	HTTP     *http.Client
	MaxBytes int64
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		HTTP:     &http.Client{Timeout: defaultTimeout},
		MaxBytes: DefaultMaxBytes,
	}
}

type Result struct {
	SHA256      string
	Bytes       []byte
	ContentType string
}

// Key 回傳物件儲存用的 key：內容雜湊加副檔名。
func (r Result) Key() string {
	ext := ".bin"
	switch {
	case strings.Contains(r.ContentType, "jpeg"):
		ext = ".jpg"
	case strings.Contains(r.ContentType, "png"):
		ext = ".png"
	case strings.Contains(r.ContentType, "webp"):
		ext = ".webp"
	case strings.Contains(r.ContentType, "gif"):
		ext = ".gif"
	}
	return r.SHA256 + ext
}

func (f *Fetcher) Fetch(ctx context.Context, url string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}

	resp, err := f.HTTP.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("下載失敗 HTTP %d", resp.StatusCode)
	}

	max := f.MaxBytes
	if max <= 0 {
		max = DefaultMaxBytes
	}
	// 多讀一個位元組才分得出「剛好等於上限」與「超過上限」
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return Result{}, err
	}
	if int64(len(data)) > max {
		return Result{}, fmt.Errorf("內容超過 %d 位元組上限", max)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return Result{}, fmt.Errorf("非圖片內容: %q", ct)
	}

	sum := sha256.Sum256(data)
	return Result{
		SHA256:      hex.EncodeToString(sum[:]),
		Bytes:       data,
		ContentType: ct,
	}, nil
}
