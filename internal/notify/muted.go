package notify

import (
	"math/rand"
	"strings"
)

// mutedLines 是靜音賣家發文時的一行通知。
//
// 刻意還是出個聲：完全不通知會產生靜默盲區，看不出他到底還有沒有在發。
// 一行的成本夠低，資訊也還在，連結仍附著以備想點進去看。
var mutedLines = []string{
	"💩 %s 又發文了，閉嘴",
	"💩 %s 洗版中，已當作沒看到",
	"💩 %s 又來了，跳過",
	"💩 %s：買我的東西啦（已靜音）",
	"💩 %s 又上架了，繼續睡",
}

// MutedMessage 產生靜音賣家的一行通知。name 為空時退回賣家編號。
//
// labels 是這則貼文命中的標籤。靜音仍然優先（還是只佔一行），
// 但把命中的標籤補在後面 —— 不然你在意的東西只因為賣家被靜音就整個漏掉。
func MutedMessage(name, permalink string, labels []string) string {
	if strings.TrimSpace(name) == "" {
		name = "某賣家"
	}
	line := mutedLines[rand.Intn(len(mutedLines))]
	msg := strings.Replace(line, "%s", name, 1)
	if len(labels) > 0 {
		msg += "（但有 " + strings.Join(labels, "、") + "）"
	}
	return msg + "  <" + permalink + ">"
}
