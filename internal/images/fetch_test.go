package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, status int, contentType string, body []byte) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestFetchHashesContent(t *testing.T) {
	body := []byte("fake-jpeg-bytes")
	s := serve(t, 200, "image/jpeg", body)

	got, err := NewFetcher().Fetch(context.Background(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if got.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("SHA256 = %q", got.SHA256)
	}
	if got.Key() != got.SHA256+".jpg" {
		t.Errorf("Key = %q，副檔名應由 content-type 決定", got.Key())
	}
}

// 同樣的內容必須得到同樣的 key —— 跨社團轉貼佔 39%，去重全靠這個。
func TestFetchIsContentAddressed(t *testing.T) {
	body := []byte("same-image")
	a := serve(t, 200, "image/png", body)
	b := serve(t, 200, "image/png", body)

	f := NewFetcher()
	ra, err := f.Fetch(context.Background(), a.URL)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := f.Fetch(context.Background(), b.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Key() != rb.Key() {
		t.Fatalf("不同網址、相同內容應得到相同 key：%s vs %s", ra.Key(), rb.Key())
	}
}

// FB 的簽章網址過期後會回非 200 或錯誤內容，不能把那些當成圖片存進去。
func TestFetchRejectsNonImage(t *testing.T) {
	s := serve(t, 200, "text/plain", []byte("URL signature expired"))
	if _, err := NewFetcher().Fetch(context.Background(), s.URL); err == nil {
		t.Fatal("非圖片內容應回報錯誤")
	}
}

func TestFetchRejectsErrorStatus(t *testing.T) {
	s := serve(t, 403, "image/jpeg", []byte("nope"))
	_, err := NewFetcher().Fetch(context.Background(), s.URL)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("應回報 HTTP 403，實得 %v", err)
	}
}

func TestFetchEnforcesSizeLimit(t *testing.T) {
	s := serve(t, 200, "image/jpeg", make([]byte, 1000))
	f := NewFetcher()
	f.MaxBytes = 500
	if _, err := f.Fetch(context.Background(), s.URL); err == nil {
		t.Fatal("超過上限應回報錯誤，否則異常大的回應會吃光記憶體")
	}
}

func TestFetchAcceptsExactlyAtLimit(t *testing.T) {
	s := serve(t, 200, "image/jpeg", make([]byte, 500))
	f := NewFetcher()
	f.MaxBytes = 500
	if _, err := f.Fetch(context.Background(), s.URL); err != nil {
		t.Fatalf("剛好等於上限應通過，實得 %v", err)
	}
}
