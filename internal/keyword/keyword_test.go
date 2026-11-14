package keyword

import (
	"strings"
	"testing"
)

func po() Rule {
	return NewRule("預購", "這是預購，不是現貨", 50,
		[]string{"預購", "預訂", "PO", "Pre-order", "代訂"})
}

func le() Rule {
	return NewRule("限量", "限量品出現了", 40,
		[]string{"限量", "限定", "LE", "Limited Edition"})
}

// 縮寫直接當子字串找會炸：兩三個字母的簡稱在英文單字裡到處都是。
// 這個測試釘住的就是那些「剛好有這兩個字母」的字。
func TestLatinAbbreviationNeedsWordBoundary(t *testing.T) {
	r := le()
	for _, s := range []string{
		"Sleeve 卡套 100 入 $120",
		"LED 燈條 展示底座 $450",
		"Bundle 套組 含特典 全新",
		"Complete Box Set 未拆 $1800",
		"Alex 代購 日版 現貨",
		"Vinyl Single 黑膠 二手",
	} {
		if r.Match(s) {
			t.Errorf("不該命中：%s", s)
		}
	}
}

// Go 的 \b 是 ASCII 定義的，中文字算非字元 ——「的LE版」兩側都成立。
func TestLatinAbbreviationMatchesBetweenChinese(t *testing.T) {
	r := le()
	for _, s := range []string{
		"收一張LE版海報 有盒",
		"想問LE的特典還有嗎？",
		"日版/韓版，LE大盒",
	} {
		if !r.Match(s) {
			t.Errorf("應該命中：%s", s)
		}
	}
}

// 大小寫不該影響。實際貼文裡小寫縮寫很常見。
func TestMatchIsCaseInsensitive(t *testing.T) {
	if !po().Match("誠收po單未開 價格私談") {
		t.Error("小寫縮寫應該命中")
	}
}

// 中文沒有詞界可用，只能子字串
func TestChinesePatternMatchesInline(t *testing.T) {
	if !po().Match("韓版專輯 預購代拍 7天內出貨") {
		t.Error("中文樣式應該命中")
	}
}

// 命中多個時全部都要出現，而且稀有的排前面
func TestMatchOrdersByPriority(t *testing.T) {
	rules := []Rule{po(), le()}
	got := Match(rules, "限量版海報 預購中 $800")
	if len(got) != 2 {
		t.Fatalf("應命中 2 個，實得 %d", len(got))
	}
	if got[0].Label != "限量" {
		t.Errorf("priority 小的應排前面，實得 %s", got[0].Label)
	}
}

func TestLinesJoinsWithNewline(t *testing.T) {
	got := Lines([]Rule{po(), le()}, "限量 預購")
	if strings.Count(got, "\n") != 1 {
		t.Errorf("兩句應分兩行: %q", got)
	}
}

// 色塊與粗體是給人看的：提示詞是我們加的，下面的原文一個字都沒動，兩者要分得開。
// 樣式加在輸出這一層，所以 line 欄位裡存的是乾淨的句子。
func TestLinesAreBarredAndBold(t *testing.T) {
	got := Lines([]Rule{po()}, "預購代拍")
	if got != "▌**這是預購，不是現貨**" {
		t.Errorf("Lines = %q", got)
	}
	for _, line := range strings.Split(Lines([]Rule{po(), le()}, "預購 限定"), "\n") {
		if !strings.HasPrefix(line, "▌") {
			t.Errorf("每一行都該有左側色塊: %q", line)
		}
	}
}

// 色塊要在粗體記號外面。手機推播會把 markdown 拿掉，** 不見之後
// 得還剩下色塊來跟貼文原文分界 —— 包在 ** 裡面就一起被吃掉了。
func TestBarSurvivesMarkdownStripping(t *testing.T) {
	line := Lines([]Rule{po()}, "預購")
	stripped := strings.ReplaceAll(line, "**", "")
	if !strings.HasPrefix(stripped, "▌") {
		t.Errorf("拿掉 markdown 後色塊要還在: %q", stripped)
	}
}

// 樣式字元不該漏進 line 欄位 —— 存的是乾淨句子，渲染才是輸出層的事
func TestRuleLineStaysPlain(t *testing.T) {
	if r := po(); strings.ContainsAny(r.Line, "*[]▌") {
		t.Errorf("規則的句子不該自己帶樣式: %q", r.Line)
	}
}

func TestNoMatchReturnsEmpty(t *testing.T) {
	if got := Lines([]Rule{po(), le()}, "壓克力立牌 現貨 $350"); got != "" {
		t.Errorf("沒命中應為空字串，實得 %q", got)
	}
	if got := Lines([]Rule{po()}, "   "); got != "" {
		t.Errorf("空白內文應為空字串，實得 %q", got)
	}
}

// 樣式清單是人手維護的，空字串與前後空白不該讓整條規則變成「什麼都中」
func TestBlankPatternsIgnored(t *testing.T) {
	r := NewRule("測試", "線", 1, []string{"", "  ", " PO "})
	if r.Match("壓克力立牌") {
		t.Error("空樣式不該讓任何文字命中")
	}
	if !r.Match("日版，PO單已截") {
		t.Error("前後空白應被修掉後仍可比對")
	}
}

func TestLabelsReturnsNames(t *testing.T) {
	got := Labels([]Rule{po(), le()}, "預購代拍")
	if len(got) != 1 || got[0] != "預購" {
		t.Errorf("Labels = %v", got)
	}
}

func TestSummonsOnlyForSummonRules(t *testing.T) {
	lim := NewRule("限量", "x", 20, []string{"限量", "Limited Edition"})
	lim.Summon = true
	pre := NewRule("預購", "y", 30, []string{"預購"})
	rules := []Rule{lim, pre}

	if !Summons(rules, "售 Limited Edition 海報") {
		t.Error("命中 summon 的關鍵字應該要叫 bot")
	}
	if Summons(rules, "售 預購單兩張") {
		t.Error("只命中沒開 summon 的關鍵字，不該叫 bot")
	}
	if Summons(rules, "售 壓克力立牌") {
		t.Error("沒命中任何關鍵字，不該叫 bot")
	}
}

// 大小寫混合的兩字母縮寫多半是別的字的開頭（人名、品牌），不該當成縮寫。
func TestShortAbbrevIsCaseSensitive(t *testing.T) {
	r := NewRule("限量", "x", 40, []string{"LE", "Limited Edition"})
	if r.Match("作者簽名 Le Guin 精裝版") {
		t.Error("Le Guin 不該命中 LE")
	}
	if !r.Match("出清 LE 特典海報") {
		t.Error("大寫 LE 應該要命中")
	}
	if !r.Match("出清 le 特典海報") {
		t.Error("全小寫 le 也應該命中")
	}
	if !r.Match("售 limited edition 盒組") {
		t.Error("較長的拉丁樣式照舊不分大小寫")
	}
}

// 沒設 pattern 時一律放行。設定從缺應該退回原本的行為，
// 不是把整個社團靜音 —— 那會是一個很難發現的靜默盲區。
func TestEmptyFilterAllowsEverything(t *testing.T) {
	for _, pats := range [][]string{nil, {}, {"", "  "}} {
		if !NewFilter(pats).Allows("隨便什麼字") {
			t.Errorf("%q 應該一律放行", pats)
		}
	}
}

func TestFilterKeepsMatchingText(t *testing.T) {
	f := NewFilter([]string{"顏料", "噴槍", "抽風"})
	for _, s := range []string{"收購水性漆顏料", "出售噴槍一支", "抽風箱便宜賣"} {
		if !f.Allows(s) {
			t.Errorf("應該通過: %q", s)
		}
	}
}

func TestFilterBlocksNonMatchingText(t *testing.T) {
	f := NewFilter([]string{"顏料", "噴槍", "抽風"})
	for _, s := range []string{
		"壓克力立牌 二手 $350 可面交",
		"今天天氣真好，隨手拍的夕陽",
	} {
		if f.Allows(s) {
			t.Errorf("不該通過: %q", s)
		}
	}
}

// 過濾跟提示共用同一套比對規則，所以拉丁字一樣有 \b 邊界。
// 沒有邊界的話 AK 會命中 Sneakers、Makeup 這類完全無關的字。
func TestFilterAppliesWordBoundaryToLatin(t *testing.T) {
	f := NewFilter([]string{"AK"})
	if f.Allows("Sneakers 球鞋 二手") {
		t.Error("Sneakers 不該被 AK 命中")
	}
	if f.Allows("Makeup Box 化妝箱") {
		t.Error("Makeup 不該被 AK 命中")
	}
	if !f.Allows("AK 軟磁片（我不會用） 90") {
		t.Error("真正的 AK 應該要命中")
	}
	if !f.Allows("一、AP、AK、AV顏料收納架") {
		t.Error("被頓號夾住的 AK 應該要命中")
	}
}

// 中文沒有詞邊界，要用子字串 —— 「水性漆」出現在句子中間一樣要算命中
func TestFilterMatchesCJKMidSentence(t *testing.T) {
	if !NewFilter([]string{"水性漆"}).Allows("因為想要收大量水性漆所以發文") {
		t.Error("中文應該用子字串比對")
	}
}
