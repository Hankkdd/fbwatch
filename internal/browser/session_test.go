package browser

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

// fakeSession 換掉開頁面、等待、判斷登出這三件事，不需要真的 Chrome。
type fakeSession struct {
	*Session
	opened    int
	openErr   error
	waitErr   error
	loggedOut bool
}

func newFake() *fakeSession {
	f := &fakeSession{}
	f.Session = &Session{
		open: func(string) (*rod.Page, error) {
			f.opened++
			if f.openErr != nil {
				return nil, f.openErr
			}
			return &rod.Page{}, nil
		},
		loggedOut: func(*rod.Page) (bool, string) {
			if f.loggedOut {
				return true, "https://www.facebook.com/checkpoint/1501092823525282/"
			}
			return false, "https://www.facebook.com/groups/1/"
		},
		waitFeed: func(*rod.Page, time.Duration, time.Duration, func(string) bool) (string, error) {
			return "<html></html>", f.waitErr
		},
		waitPage: func(*rod.Page, time.Duration, func(string) bool) (string, error) {
			return "<html></html>", f.waitErr
		},
	}
	return f
}

var errTimeout = errors.New("等待 feed 渲染逾時（60s 內未出現任何貼文單元）")

// 被導向登入頁時 feed 永遠不會出現，等待一定逾時。以前呼叫端先處理逾時錯誤，
// 登出檢查根本跑不到，登出就被當成普通失敗，每個社團各打一次、冷卻後再打一次。
func TestLoadDetectsLogoutEvenWhenWaitTimesOut(t *testing.T) {
	for name, load := range map[string]func(*Session) (*rod.Page, string, error){
		"Load":     func(s *Session) (*rod.Page, string, error) { return s.Load("u", time.Minute, 0, nil) },
		"LoadPage": func(s *Session) (*rod.Page, string, error) { return s.LoadPage("u", time.Minute, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.waitErr, f.loggedOut = errTimeout, true

			page, _, err := load(f.Session)
			if !errors.Is(err, ErrSessionInvalid) {
				t.Fatalf("err = %v, want ErrSessionInvalid", err)
			}
			if !strings.Contains(err.Error(), "/checkpoint/") {
				t.Errorf("err = %v, want the redirect URL in it", err)
			}
			if page == nil {
				t.Error("失效時也要回傳分頁，呼叫端才關得掉")
			}
			if !errors.Is(f.Err(), ErrSessionInvalid) {
				t.Error("Session 應已失效")
			}
		})
	}
}

// 這是整個 Session 存在的理由：失效後任何路徑都不能再開 FB 頁面。
// 重試會把軟性封鎖升級成硬封鎖。
func TestInvalidSessionNeverOpensAnotherPage(t *testing.T) {
	f := newFake()
	f.loggedOut = true
	if _, _, err := f.Load("u", time.Minute, 0, nil); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("first load err = %v", err)
	}
	f.loggedOut = false // 就算之後看起來正常，也不能再試

	for range 3 {
		page, _, err := f.Load("u", time.Minute, 0, nil)
		if !errors.Is(err, ErrSessionInvalid) || page != nil {
			t.Fatalf("Load after invalid = (%v, %v)", page, err)
		}
		page, _, err = f.LoadPage("u", time.Minute, nil)
		if !errors.Is(err, ErrSessionInvalid) || page != nil {
			t.Fatalf("LoadPage after invalid = (%v, %v)", page, err)
		}
	}
	if f.opened != 1 {
		t.Errorf("opened %d pages, want 1: an invalid session must not open any more", f.opened)
	}
}

// 偶發逾時不能當成登出，否則一次網路不穩就熔斷、要人工重啟。
func TestTransientFailureKeepsSessionValid(t *testing.T) {
	f := newFake()
	f.waitErr = errTimeout

	page, _, err := f.LoadPage("u", time.Minute, nil)
	if !errors.Is(err, errTimeout) || errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("err = %v, want the timeout as-is", err)
	}
	if page == nil {
		t.Error("逾時也要回傳分頁，呼叫端才關得掉")
	}
	if f.Err() != nil {
		t.Fatalf("Session 不該失效: %v", f.Err())
	}

	f.waitErr = nil
	if _, _, err := f.LoadPage("u", time.Minute, nil); err != nil {
		t.Fatalf("next load err = %v", err)
	}
	if f.opened != 2 {
		t.Errorf("opened %d pages, want 2", f.opened)
	}
}

// 分頁都沒開成就沒有網址可看，不能據此判定登出。
func TestOpenFailureKeepsSessionValid(t *testing.T) {
	f := newFake()
	f.openErr = errors.New("CDP 連線中斷")

	if _, _, err := f.Load("u", time.Minute, 0, nil); err == nil || errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("err = %v, want the open error", err)
	}
	if f.Err() != nil {
		t.Fatalf("Session 不該失效: %v", f.Err())
	}
}

func TestLogoutWithoutURLStillInvalidates(t *testing.T) {
	f := newFake()
	f.Session.loggedOut = func(*rod.Page) (bool, string) { return true, "" }

	if _, _, err := f.Load("u", time.Minute, 0, nil); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(f.Err(), ErrSessionInvalid) {
		t.Error("拿不到網址也要失效")
	}
}

// 留言查詢與輪詢跑在不同 goroutine，共用同一個 Session。
func TestErrIsSafeAcrossGoroutines(t *testing.T) {
	s := &Session{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); s.invalidate("https://www.facebook.com/login/") }()
		go func() { defer wg.Done(); _ = s.Err() }()
	}
	wg.Wait()
	if !errors.Is(s.Err(), ErrSessionInvalid) {
		t.Fatal("should be invalid")
	}
}
