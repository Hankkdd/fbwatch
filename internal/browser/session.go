package browser

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-rod/rod"
)

// Session 是對 FB 開頁面的唯一入口，負責「登入態一失效就不再碰 FB」。
//
// 這件事以前散在呼叫端：每個呼叫端要自己在載入後檢查 LoggedOut，再自己把
// 錯誤分類給熔斷。結果三條路徑都沒做對 —— 被導向登入頁時 feed 不會出現，
// 載入一定是等到逾時才回來，而呼叫端先處理了逾時錯誤，LoggedOut 根本沒跑到；
// 後來加的留言查詢與補抓內文則是完全沒接上熔斷。
//
// 所以檢查與停手都收進這裡，呼叫端拿不到繞過的方法：
//   - 每次載入後，不論等待成功與否，都先看有沒有被導向登入或 checkpoint
//   - 一旦被導向，Session 永久失效，之後的載入直接回 ErrSessionInvalid，連分頁都不開
//
// 失效不會自己恢復。重新登入後要重啟行程，這與熔斷的 halt 一致。
type Session struct {
	mu sync.Mutex
	// redirectedTo 非空表示已失效，記的是被導向的網址。
	redirectedTo string

	open      func(url string) (*rod.Page, error)
	loggedOut func(*rod.Page) (bool, string)
	waitFeed  func(page *rod.Page, timeout, settle time.Duration, ready func(string) bool) (string, error)
	waitPage  func(page *rod.Page, timeout time.Duration, ready func(string) bool) (string, error)
}

// NewSession 包住一個已連上的 Chrome。同一個 Chrome 的所有使用者要共用同一個 Session，
// 否則一條路徑偵測到失效，其他路徑還會繼續開頁面。
func NewSession(b *rod.Browser) *Session {
	return &Session{
		open:      func(url string) (*rod.Page, error) { return newPage(b, url) },
		loggedOut: LoggedOut,
		waitFeed:  WaitForFeed,
		waitPage:  waitForPage,
	}
}

// Load 開動態牆頁面，等法同 WaitForFeed。呼叫端負責關閉回傳的分頁，失效時也一樣。
func (s *Session) Load(url string, timeout, settle time.Duration, ready func(string) bool) (*rod.Page, string, error) {
	return s.load(url, func(p *rod.Page) (string, error) { return s.waitFeed(p, timeout, settle, ready) })
}

// LoadPage 開動態牆以外的頁面，等到 ready 成立。呼叫端負責關閉回傳的分頁，失效時也一樣。
func (s *Session) LoadPage(url string, timeout time.Duration, ready func(string) bool) (*rod.Page, string, error) {
	return s.load(url, func(p *rod.Page) (string, error) { return s.waitPage(p, timeout, ready) })
}

func (s *Session) load(url string, wait func(*rod.Page) (string, error)) (*rod.Page, string, error) {
	if err := s.Err(); err != nil {
		return nil, "", err
	}
	page, err := s.open(url)
	if err != nil {
		return nil, "", err
	}
	doc, waitErr := wait(page)
	// 先看有沒有被導向，再看等待結果：登入頁沒有要等的內容，一定是逾時才回來。
	if out, to := s.loggedOut(page); out {
		s.invalidate(to)
		return page, doc, s.Err()
	}
	return page, doc, waitErr
}

func (s *Session) invalidate(redirectedTo string) {
	// 空字串代表「還有效」，網址拿不到也要記成非空
	if redirectedTo == "" {
		redirectedTo = "（網址不明）"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.redirectedTo == "" {
		s.redirectedTo = redirectedTo
	}
}

// Err 在 Session 失效後回傳包著 ErrSessionInvalid 的錯誤，否則回傳 nil。
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.redirectedTo == "" {
		return nil
	}
	return fmt.Errorf("%w：被導向 %s", ErrSessionInvalid, s.redirectedTo)
}
