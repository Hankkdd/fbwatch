package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"golang.org/x/net/html"

	"fbwatch/internal/browser"
	"fbwatch/internal/parse"
	"fbwatch/internal/store"
)

// browserMu 讓輪詢、補抓內文與留言查詢輪流用同一個 Chrome。
//
// 留言是應比價 bot 要求才抓的，跟主迴圈是不同 goroutine。不排隊的話會同時
// 開兩個分頁打 FB，請求節奏跟人不一樣；排隊的話，最多等主迴圈抓完手上那一頁。
var browserMu sync.Mutex

// halted 在熔斷後設起來。熔斷代表登入失效或 FB 在擋，這時候任何頁面都不該再開，
// 包括留言查詢 —— 不然熔斷就被這條路繞過去了。
var halted atomic.Bool

const (
	// commentsGap 是兩次實際開頁之間至少隔多久。這條路是模型觸發的，要有上限。
	commentsGap = 30 * time.Second
	// commentsDeadline 是一次查詢的總時限，包含排隊。MCP 呼叫 60 秒就逾時，
	// 這裡要先放棄，讓 bot 拿到「稍後再試」而不是一個看不懂的逾時錯誤。
	commentsDeadline = 45 * time.Second
	// commentsSettle 是貼文本體出現後，再等留言渲染的時間。
	commentsSettle = 6 * time.Second
)

var reListingID = regexp.MustCompile(`^\d{8,25}$`)

type commentsResponse struct {
	ListingID string          `json:"listing_id"`
	FetchedAt time.Time       `json:"fetched_at"`
	Total     int             `json:"total"`
	Comments  []parse.Comment `json:"comments"`
}

type commentServer struct {
	st interface {
		Permalink(ctx context.Context, id string) (string, error)
	}
	b     *rod.Browser
	token string

	mu   sync.Mutex
	last time.Time // 上次實際開頁的時間
	load func(ctx context.Context, url string) (string, error)
}

func newCommentServer(st interface {
	Permalink(ctx context.Context, id string) (string, error)
}, b *rod.Browser, token string) *commentServer {
	s := &commentServer{st: st, b: b, token: token}
	s.load = s.loadPage
	return s
}

// errBusy 表示排隊等不到瀏覽器，請對方稍後再問。
var errBusy = errors.New("瀏覽器忙碌中")

func (s *commentServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := r.URL.Query().Get("id")
	if !reListingID.MatchString(id) {
		http.Error(w, "id 必須是貼文數字 ID", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), commentsDeadline)
	defer cancel()
	resp, err := s.get(ctx, id)
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "fbwatch 沒有收錄這篇貼文", http.StatusNotFound)
	case errors.Is(err, errBusy), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "瀏覽器忙碌中，請稍後再試", http.StatusServiceUnavailable)
	case errors.Is(err, errHalted):
		http.Error(w, "監控已熔斷（登入失效或被擋），暫停開任何 FB 頁面", http.StatusServiceUnavailable)
	default:
		log.Printf("留言查詢 %s 失敗: %v", id, err)
		http.Error(w, "抓取失敗："+firstLine(err.Error()), http.StatusBadGateway)
	}
}

var errHalted = errors.New("halted")

func (s *commentServer) get(ctx context.Context, id string) (commentsResponse, error) {
	// 不快取：留言是「現在還在不在」的依據，每次問都重抓
	if halted.Load() {
		return commentsResponse{}, errHalted
	}
	permalink, err := s.st.Permalink(ctx, id)
	if err != nil {
		return commentsResponse{}, err
	}

	if !lockWithContext(ctx, &browserMu) {
		return commentsResponse{}, errBusy
	}
	defer browserMu.Unlock()
	if halted.Load() { // 排隊期間主迴圈可能剛熔斷
		return commentsResponse{}, errHalted
	}

	// 兩次開頁之間的最小間隔，加一點隨機，不要像機器一樣準時
	s.mu.Lock()
	wait := time.Until(s.last.Add(commentsGap + time.Duration(rand.Intn(5000))*time.Millisecond))
	s.mu.Unlock()
	if wait > 0 {
		select {
		case <-ctx.Done():
			return commentsResponse{}, errBusy
		case <-time.After(wait):
		}
	}
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()

	doc, err := s.load(ctx, permalink)
	if err != nil {
		return commentsResponse{}, err
	}
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return commentsResponse{}, err
	}
	c := parse.ParseComments(root)
	resp := commentsResponse{ListingID: id, FetchedAt: time.Now(), Total: c.Total, Comments: c.List}
	if resp.Comments == nil {
		resp.Comments = []parse.Comment{}
	}
	log.Printf("留言查詢 %s：%d 則（標示共 %d 則）", id, len(c.List), c.Total)
	return resp, nil
}

// loadPage 用已登入的 Chrome 開貼文頁，等本體出現後再給留言一點時間渲染。
func (s *commentServer) loadPage(ctx context.Context, url string) (string, error) {
	ready := func(doc string) bool {
		return strings.Contains(doc, "redacted_description") || strings.Contains(doc, `"message":{"text"`)
	}
	remaining := time.Until(deadlineOr(ctx, time.Now().Add(commentsDeadline))) - commentsSettle - 2*time.Second
	if remaining < 10*time.Second {
		return "", errBusy
	}
	page, _, err := browser.LoadPage(s.b, url, remaining, ready)
	defer browser.ClosePage(page)
	if err != nil {
		return "", err
	}
	if out, to := browser.LoggedOut(page); out {
		return "", errors.New("被導向登入頁：" + to)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(commentsSettle):
	}
	return browser.OuterHTML(page)
}

func deadlineOr(ctx context.Context, def time.Time) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return def
}

// lockWithContext 在 ctx 到期前拿到鎖就回 true。
func lockWithContext(ctx context.Context, mu *sync.Mutex) bool {
	for {
		if mu.TryLock() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// serveComments 在內網開留言查詢端點。只給比價 bot 的 MCP 服務用，
// 沒設 token 就不開（不提供「沒 token 就放行」這條路）。
func serveComments(addr string, s *commentServer) {
	if s.token == "" {
		log.Print("未設 FBWATCH_MCP_TOKEN，不提供留言查詢")
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/comments", s)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("留言查詢端點 %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("留言查詢端點停止: %v", err)
	}
}
