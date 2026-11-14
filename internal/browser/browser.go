// Package browser 封裝與既有 Chrome 的互動。
//
// collect 與 dumpdom 都走這裡，不要各自實作 —— 兩邊的等待條件曾經分岔，
// 導致其中一支永遠逾時。
package browser

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

// GroupURL 回傳社團的時間序動態網址。
// 少了 sorting_setting，買賣社團會落在演算法排序的商品分頁，第一屏都是舊內容。
func GroupURL(gid string) string {
	return fmt.Sprintf("https://www.facebook.com/groups/%s/?sorting_setting=CHRONOLOGICAL", gid)
}

// CallTimeout 是單一組 CDP 往返的上限。
//
// 沒有這個上限的話，一個不回應的呼叫會把整支程式卡死：2026-11-09 21:03
// 主 goroutine 停在 DOMGetDocument 整整 89 分鐘，進程活著、Chrome 活著、
// CDP 端點還會回應，所以 Docker 不重啟、熔斷器也數不到任何失敗。
//
// 20 秒是「慢得不合理」而不是「慢」：正常的 DOM 快照是毫秒級，
// 最慢的實測也在 2 秒內。設這麼寬只是不想在機器忙的時候誤殺。
const CallTimeout = 20 * time.Second

// OuterHTML 取渲染後的 DOM。
//
// 刻意不用 Runtime.evaluate —— 不注入任何 JS，避開 CDP 最主要的偵測向量。
// 用 BackendNodeID 而非 NodeID：FB 的 DOM 持續變動，NodeID 會在兩次呼叫之間失效，
// 表現為 "Could not find node with given id"。
//
// 兩個呼叫共用一份逾時預算。它們是同一件事的兩半，分開計時沒有意義。
func OuterHTML(page *rod.Page) (string, error) {
	p := page.Timeout(CallTimeout)
	defer p.CancelTimeout()

	doc, err := proto.DOMGetDocument{Depth: gson.Int(-1), Pierce: true}.Call(p)
	if err != nil {
		return "", err
	}
	res, err := proto.DOMGetOuterHTML{BackendNodeID: doc.Root.BackendNodeID}.Call(p)
	if err != nil {
		return "", err
	}
	return res.OuterHTML, nil
}

// ClosePage 關閉分頁，同樣要有上限 —— 關閉也是 CDP 呼叫，一樣會卡住。
func ClosePage(page *rod.Page) {
	if page == nil {
		return
	}
	p := page.Timeout(CallTimeout)
	defer p.CancelTimeout()
	_ = p.Close()
}

// baseSettle 是看到第一個貼文單元後的無條件等待。
// 相鄰的幾則與時間戳記節點都比容器晚出現，立刻截取會少抓。
const baseSettle = 5 * time.Second

// WaitForFeed 等到動態牆渲染出真實貼文單元。
//
// 貼文的判斷依據是 data-virtualized 出現，不是 loading-state 消失 ——
// 虛擬列表底下永遠留著未渲染項目的骨架，等它們全部消失的條件永遠不會成立。
//
// ready 由呼叫端提供，用來表達「資料齊了」。browser 套件不該自己猜：
// 曾經用正則在整份 DOM 找時間字樣，結果命中了選單之類的無關文字而提早收手。
// ready 為 nil，或在 settle 內始終不成立，都照常回傳目前的 DOM，不視為失敗。
func WaitForFeed(page *rod.Page, timeout, settle time.Duration, ready func(string) bool) (string, error) {
	deadline := time.Now().Add(timeout)
	var doc string

	for {
		var err error
		doc, err = OuterHTML(page)
		if err != nil {
			// DOM 變動造成的暫時性失敗，重試即可
			if time.Now().After(deadline) {
				return "", err
			}
			time.Sleep(time.Second)
			continue
		}
		if strings.Contains(doc, "data-virtualized") {
			break
		}
		if time.Now().After(deadline) {
			return doc, fmt.Errorf("等待 feed 渲染逾時（%s 內未出現任何貼文單元）", timeout)
		}
		time.Sleep(time.Second)
	}

	time.Sleep(baseSettle)
	if d, err := OuterHTML(page); err == nil {
		doc = d
	}
	if ready == nil {
		return doc, nil
	}

	extra := time.Now().Add(settle)
	for !ready(doc) && time.Now().Before(extra) {
		time.Sleep(2 * time.Second)
		if d, err := OuterHTML(page); err == nil {
			doc = d
		}
	}
	return doc, nil
}

// Load 開新分頁、導向、等渲染，回傳 DOM。呼叫端負責關閉分頁。
func Load(b *rod.Browser, url string, timeout, settle time.Duration, ready func(string) bool) (*rod.Page, string, error) {
	page, err := newPage(b, url)
	if err != nil {
		return nil, "", err
	}

	doc, err := WaitForFeed(page, timeout, settle, ready)
	return page, doc, err
}

// LoadPage 導向任意網址並等到 ready 成立，用於動態牆以外的頁面。
//
// 與 Load 分開是因為 Load 會先等 data-virtualized（虛擬列表的標記），
// 那只存在於動態牆；商品詳情頁沒有，會白等到逾時。
func LoadPage(b *rod.Browser, url string, timeout time.Duration, ready func(string) bool) (*rod.Page, string, error) {
	page, err := newPage(b, url)
	if err != nil {
		return nil, "", err
	}

	deadline := time.Now().Add(timeout)
	var doc string
	for {
		if d, err := OuterHTML(page); err == nil {
			doc = d
			if ready == nil || ready(doc) {
				return page, doc, nil
			}
		}
		if time.Now().After(deadline) {
			return page, doc, fmt.Errorf("等待頁面就緒逾時（%s）", timeout)
		}
		time.Sleep(time.Second)
	}
}

// newPage 開一個分頁並導向 url。導向與拉到前景都有上限 ——
// 會卡住的不是只有讀 DOM。
//
// 開分頁（b.Page）刻意不包逾時：那會讓回傳的 page 繼承一個 20 秒就到期的
// context，後面 WaitForFeed 最長要等 60 秒，會被自己的保護砍掉。
// 要正確處理得把 page 的 context 重新綁回母 context，語意微妙又難驗證 ——
// 綁錯的話一般輪詢（約 10 秒）看不出來，只有 60 秒的內文補抓會壞。
// 開分頁卡住的風險交給看門狗，那一層本來就是為了擋這種沒想到的卡點。
func newPage(b *rod.Browser, url string) (*rod.Page, error) {
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, err
	}
	p := page.Timeout(CallTimeout)
	defer p.CancelTimeout()

	if _, err := (proto.PageNavigate{URL: url}).Call(p); err != nil {
		ClosePage(page)
		return nil, err
	}
	// 背景分頁 Chrome 不渲染，FB 的 feed 會永遠停在骨架狀態
	_ = (proto.PageBringToFront{}).Call(p)
	return page, nil
}

// ErrSessionInvalid 表示登入態失效或帳號被 checkpoint。
//
// 這類失敗絕不能重試：重試會把軟性封鎖升級成硬封鎖，
// 而且恢復需要人工重新登入，機器再試幾次都沒有意義。
var ErrSessionInvalid = errors.New("登入態失效")

// LoggedOut 判斷是否被導向登入或 checkpoint。
func LoggedOut(page *rod.Page) (bool, string) {
	p := page.Timeout(CallTimeout)
	defer p.CancelTimeout()
	info, err := p.Info()
	if err != nil {
		return false, ""
	}
	if strings.Contains(info.URL, "/login") || strings.Contains(info.URL, "/checkpoint") {
		return true, info.URL
	}
	return false, info.URL
}
