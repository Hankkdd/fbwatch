package parse

import (
	"regexp"
	"strconv"
	"strings"
)

type Status string

const (
	StatusSelling  Status = "selling"
	StatusWanted   Status = "wanted"
	StatusSold     Status = "sold"
	StatusReserved Status = "reserved"
)

// WantedSentinel 是社團慣例：徵求貼文把結構化標價填成 NT$9,999。
const WantedSentinel = 9999

var statusKeywords = []struct {
	status Status
	words  []string
}{
	// 順序即優先序：已售出蓋過保留，保留蓋過徵求。
	{StatusSold, []string{"已售出", "已售", "售出", "完售", "已賣", "已收"}},
	{StatusReserved, []string{"已保留", "保留中", "保留"}},
	{StatusWanted, []string{"徵求", "收購", "求購", "徵收", "誠徵", "徵"}},
}

// DetectStatus 由內文判定狀態。ntTag 是結構化 NT$ 欄位，只作為輔助訊號。
func DetectStatus(body string, ntTag int) Status {
	for _, k := range statusKeywords {
		for _, w := range k.words {
			if strings.Contains(body, w) {
				return k.status
			}
		}
	}
	if ntTag == WantedSentinel {
		return StatusWanted
	}
	return StatusSelling
}

var (
	cnDigits = map[rune]int{'零': 0, '一': 1, '二': 2, '兩': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	cnUnits = map[rune]int{'十': 10, '百': 100, '千': 1000, '萬': 10000}

	reCNNum  = regexp.MustCompile(`[零一二兩三四五六七八九十百千萬]+`)
	reArabic = regexp.MustCompile(`[\d][\d,]*`)
)

// ChineseToInt 解析中文數字，支援口語省略式（兩千五 = 2500、三百五 = 350）。
// 無法解析時回傳 -1。
func ChineseToInt(s string) int {
	total, section, number, lastUnit := 0, 0, 0, 0
	seen := false

	for _, r := range s {
		if d, ok := cnDigits[r]; ok {
			number = d
			seen = true
			continue
		}
		u, ok := cnUnits[r]
		if !ok {
			return -1
		}
		seen = true
		if u == 10000 {
			section = (section + number) * u
			total += section
			section, number = 0, 0
			lastUnit = u // 「一萬五」的五落在千位，所以不能清掉
			continue
		}
		if number == 0 {
			number = 1 // 「十五」的十
		}
		section += number * u
		number = 0
		lastUnit = u
	}
	if !seen {
		return -1
	}
	// 尾數省略：「兩千五」的五落在百位
	if number > 0 && lastUnit >= 100 {
		return total + section + number*(lastUnit/10)
	}
	return total + section + number
}

type priceCandidate struct {
	value int
	score int
}

// 評分規則。數字本身不夠判斷，要看上下文。
// 這些權重預期會隨真實資料調整，所以每條都有對應測試。
func scoreCandidate(text string, start, end, value int) int {
	before := text[:start]
	after := text[end:]
	score := 0

	if strings.HasSuffix(before, "+") || strings.HasSuffix(before, "＋") {
		score -= 5 // 「+60元」是運費加價，不是售價
	}
	if hasPrefixAny(after, "元", "塊", "圓") {
		score += 3
	}
	if hasSuffixAny(before, "NT$", "$", "＄") {
		score += 3
	}
	if hasPrefixAny(after, "版", "樓", "月", "年", "日", "號", "折") {
		score -= 10 // 「第11版」不是價格
	}
	if hasPrefixAny(after, "套", "組", "個", "隻", "張", "件", "盒", "入") {
		score -= 4 // 數量詞
	}
	if hasSuffixAny(before, "售", "賣", "價", "要", "共", "含") {
		score += 2
	}
	if value >= 50 && value <= 100000 {
		score += 1 // 合理的成交價區間
	}
	if value == WantedSentinel {
		score -= 8
	}
	return score
}

// ExtractPrice 從內文取價格，回傳 -1 表示抓不到。
// 內文是主來源；結構化的 NT$ 欄位不可信（常是佔位值）。
func ExtractPrice(body string) int {
	var cands []priceCandidate

	for _, loc := range reArabic.FindAllStringIndex(body, -1) {
		raw := body[loc[0]:loc[1]]
		v, err := strconv.Atoi(strings.ReplaceAll(raw, ",", ""))
		if err != nil {
			continue
		}
		cands = append(cands, priceCandidate{v, scoreCandidate(body, loc[0], loc[1], v)})
	}

	for _, loc := range reCNNum.FindAllStringIndex(body, -1) {
		v := ChineseToInt(body[loc[0]:loc[1]])
		if v <= 0 {
			continue
		}
		s := scoreCandidate(body, loc[0], loc[1], v)
		s += 2 // 中文數字幾乎只用來寫價格，很少是型號或數量
		cands = append(cands, priceCandidate{v, s})
	}

	best, bestScore := -1, -1<<31
	for _, c := range cands {
		// 同分取較大值：成交價通常大於運費、版本號等雜訊
		if c.score > bestScore || (c.score == bestScore && c.value > best) {
			best, bestScore = c.value, c.score
		}
	}
	return best
}

func hasPrefixAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func hasSuffixAny(s string, suffixes ...string) bool {
	for _, p := range suffixes {
		if strings.HasSuffix(s, p) {
			return true
		}
	}
	return false
}
