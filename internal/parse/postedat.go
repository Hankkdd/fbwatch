package parse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FB 的貼文時間以相對字串呈現（「3分鐘」「6小時」），
// 而且不在貼文容器內 —— 它住在 body 底下的隱藏節點，
// 由容器的 aria-labelledby 反向引用。見 Articles 的實作。

var reRelative = regexp.MustCompile(`^\s*(?:(\d+)\s*(分鐘|小時|天|週|年)|(剛剛|現在))\s*$`)

// ParseRelativeTime 把 FB 的相對時間字串換算成絕對時間。
// 第二個回傳值表示是否解析成功。
//
// 精度受限於 FB 自己的呈現：超過一小時的貼文只有小時級精度。
// 對延遲量測而言夠用 —— 剛發出的貼文會顯示分鐘級。
func ParseRelativeTime(s string, now time.Time) (time.Time, bool) {
	m := reRelative.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	if m[3] != "" {
		return now, true
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return time.Time{}, false
	}
	var d time.Duration
	switch m[2] {
	case "分鐘":
		d = time.Duration(n) * time.Minute
	case "小時":
		d = time.Duration(n) * time.Hour
	case "天":
		d = time.Duration(n) * 24 * time.Hour
	case "週":
		d = time.Duration(n) * 7 * 24 * time.Hour
	case "年":
		d = time.Duration(n) * 365 * 24 * time.Hour
	default:
		return time.Time{}, false
	}
	return now.Add(-d), true
}

// labelIndex 是 id → 文字 的對照表，用來解析 aria-labelledby 的引用。
type labelIndex map[string]string

// resolvePostedAt 從 aria-labelledby 的 id 清單中找出相對時間字串。
func resolvePostedAt(idList string, idx labelIndex, now time.Time) (time.Time, bool) {
	for _, id := range strings.Fields(idList) {
		text, ok := idx[id]
		if !ok {
			continue
		}
		if t, ok := ParseRelativeTime(text, now); ok {
			return t, true
		}
	}
	return time.Time{}, false
}
