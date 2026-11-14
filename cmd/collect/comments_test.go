package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fbwatch/internal/store"
)

type fakePermalinks map[string]string

func (f fakePermalinks) Permalink(_ context.Context, id string) (string, error) {
	if p, ok := f[id]; ok {
		return p, nil
	}
	return "", store.ErrNotFound
}

const pageWithComment = `<div role="article" aria-label="買家乙的留言3小時前"><span dir="auto">買家乙</span><span dir="auto">3小時</span><div dir="auto">還在嗎</div></div>`

func newTestServer(loads *[]string) *commentServer {
	s := newCommentServer(fakePermalinks{"1234567890": "https://www.facebook.com/groups/1/posts/1234567890/"}, nil, "tok")
	s.load = func(_ context.Context, url string) (string, error) {
		*loads = append(*loads, url)
		return pageWithComment, nil
	}
	return s
}

func call(s *commentServer, auth, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/comments?id="+id, nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestCommentsAuthAndValidation(t *testing.T) {
	var loads []string
	s := newTestServer(&loads)
	if w := call(s, "", "1234567890"); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := call(s, "Bearer tok", "https://evil.example/"); w.Code != http.StatusBadRequest {
		t.Errorf("url as id: %d", w.Code)
	}
	if w := call(s, "Bearer tok", "9999999999"); w.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", w.Code)
	}
	if len(loads) != 0 {
		t.Errorf("no page should be opened, got %v", loads)
	}
}

func TestCommentsFetchesPermalinkEveryTime(t *testing.T) {
	var loads []string
	s := newTestServer(&loads)
	w := call(s, "Bearer tok", "1234567890")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "還在嗎") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if len(loads) != 1 || loads[0] != "https://www.facebook.com/groups/1/posts/1234567890/" {
		t.Errorf("loads = %v", loads)
	}
	s.last = time.Time{} // 略過最小間隔，只驗證不快取
	call(s, "Bearer tok", "1234567890")
	if len(loads) != 2 {
		t.Errorf("second call should refetch, loads = %v", loads)
	}
}

func TestCommentsRefusedWhenHalted(t *testing.T) {
	var loads []string
	s := newTestServer(&loads)
	halted.Store(true)
	defer halted.Store(false)
	if w := call(s, "Bearer tok", "1234567890"); w.Code != http.StatusServiceUnavailable || len(loads) != 0 {
		t.Errorf("halted: %d, loads %v", w.Code, loads)
	}
}

func TestCommentsBusyWhenBrowserHeld(t *testing.T) {
	var loads []string
	s := newTestServer(&loads)
	browserMu.Lock()
	defer browserMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := s.get(ctx, "1234567890"); err != errBusy {
		t.Errorf("err = %v, want errBusy", err)
	}
}
