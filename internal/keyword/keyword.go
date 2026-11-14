// Package keyword 判斷貼文有沒有提到在意的字，並給出對應的提示詞。
//
// 比對本身不難，難的是縮寫。社團裡兩三個字母的簡稱很常見，但直接當子字串
// 找會炸：拿 464 則歷史貼文實測，某個兩字母縮寫命中 16 則，其中 13 則是
// 英文單字裡剛好有這兩個字母（Sleeve、LED、Bundle 這類），跟它無關。
//
// 所以拉丁字母的樣式一律加 \b 邊界。Go 的 \b 是 ASCII 定義的，
// 中文字算非字元，「的LE版」兩側都成立、「Sleeve」兩側都不成立 ——
// 剛好就是我們要的行為。中文樣式則直接子字串，中文沒有詞界可用。
package keyword

import (
	"regexp"
	"sort"
	"strings"
)

// Rule 是一個在意的主題：一組同義詞，配一句提示詞。
type Rule struct {
	Label    string // 限量
	Line     string // 限量品出現了
	Priority int    // 小的排前面；命中多個時稀有的先講
	Patterns []string
	Summon   bool // 命中時 tag 比價 bot 出來比價

	latin []*regexp.Regexp
	cjk   []string
}

var (
	allLatin    = regexp.MustCompile(`^[A-Za-z][A-Za-z ]*$`)
	shortAbbrev = regexp.MustCompile(`^[A-Z]{2,3}$`)
)

func isShortAbbrev(p string) bool { return shortAbbrev.MatchString(p) }

// NewRule 預先編好樣式。比對每則貼文都會跑，不要每次重編。
func NewRule(label, line string, priority int, patterns []string) Rule {
	r := Rule{Label: label, Line: line, Priority: priority, Patterns: patterns}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if allLatin.MatchString(p) {
			// 兩三個字母的全大寫縮寫：只認全大寫或全小寫，不認大小寫混合。
			// 用 1300 多則歷史資料比對：不分大小寫時會多咬到以該兩字母開頭的
			// 人名與品牌（「Le Guin」之類），而且自動叫了比價 bot。真的在講
			// 縮寫的人不是全大寫就是全小寫，不會寫成首字大寫。
			// 其他較長的拉丁樣式照舊不分大小寫。
			expr := `(?i)\b` + regexp.QuoteMeta(p) + `\b`
			if isShortAbbrev(p) {
				expr = `\b(?:` + regexp.QuoteMeta(p) + `|` + regexp.QuoteMeta(strings.ToLower(p)) + `)\b`
			}
			r.latin = append(r.latin, regexp.MustCompile(expr))
		} else {
			r.cjk = append(r.cjk, p)
		}
	}
	return r
}

func (r Rule) Match(text string) bool {
	for _, p := range r.cjk {
		if strings.Contains(text, p) {
			return true
		}
	}
	for _, re := range r.latin {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// Match 回傳所有命中的規則，依 priority 由小到大。
//
// 不只取第一個：一則貼文同時命中兩個主題時，兩句都該出現 ——
// 實測 464 則裡有 18 則命中多個，最多也才三個，不會洗版。
func Match(rules []Rule, text string) []Rule {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []Rule
	for _, r := range rules {
		if r.Match(text) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// bar 是提示行左邊那根色塊。
//
// 要跟貼文原文分清楚 —— 提示詞是我們加的，原文一個字都沒動，
// 視覺上不該混在一起。粗體（Discord 的 **）在卡片裡夠用，但手機推播
// 會把 markdown 拿掉，只剩光溜溜的一行文字，跟內文黏成一片。
// 色塊是純文字，推播列上活得下來，所以它才是真正在做區隔的東西。
const bar = "▌"

// Lines 把命中的規則排成要放在通知最上面的那幾行。
//
// 樣式在這裡加，不是存在 line 欄位裡：改句子的人不用記得補，
// 想換整體樣式也只要動這一個地方。
func Lines(rules []Rule, text string) string {
	m := Match(rules, text)
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for _, r := range m {
		parts = append(parts, bar+"**"+r.Line+"**")
	}
	return strings.Join(parts, "\n")
}

// Labels 回傳命中的標籤，給靜音賣家那種只剩一行的場合用。
func Labels(rules []Rule, text string) []string {
	m := Match(rules, text)
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, r := range m {
		out = append(out, r.Label)
	}
	return out
}

// Summons 回報這段文字有沒有命中任何要叫比價 bot 的規則。
func Summons(rules []Rule, text string) bool {
	for _, r := range Match(rules, text) {
		if r.Summon {
			return true
		}
	}
	return false
}

// Filter 判斷一段文字有沒有命中任一個 pattern，比對規則與關鍵字提示完全相同：
// 全拉丁字加 \b 邊界，其餘用子字串。共用同一套邏輯是刻意的 ——
// 兩邊各寫一次，遲早會在其中一邊發現「AK 為什麼會命中 Sneaky」。
type Filter struct {
	rule  Rule
	empty bool
}

// NewFilter 建立過濾器。沒有 pattern 時一律放行，不是一律擋下 ——
// 設定從缺應該退回「原本的行為」，不是把整個社團靜音。
func NewFilter(patterns []string) Filter {
	var kept []string
	for _, p := range patterns {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return Filter{empty: true}
	}
	return Filter{rule: NewRule("filter", "", 0, kept)}
}

// Allows 回報這段文字要不要通知。
func (f Filter) Allows(text string) bool {
	return f.empty || f.rule.Match(text)
}
