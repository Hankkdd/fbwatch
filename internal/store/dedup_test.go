package store

import (
	"fmt"
	"testing"
	"time"
)

func pend(contentKey string, final bool) Pending {
	return Pending{ID: "self", SellerID: "s1", FeedKey: "F", ContentKey: contentKey, BodyFinal: final}
}

func TestNoCandidatesMeansSend(t *testing.T) {
	if v := decideDup(pend("C", true), nil); v != DupSend {
		t.Errorf("沒有疑似對象就該送，實得 %v", v)
	}
}

// 判準是完整內文，不是 feed
func TestSameContentKeyIsSuppressed(t *testing.T) {
	c := []dupCandidate{{ID: "prior", ContentKey: "C", BodyFinal: true}}
	if v := decideDup(pend("C", true), c); v != DupSuppress {
		t.Errorf("完整內文相同就該擋，實得 %v", v)
	}
	if got := pickDup(pend("C", true), c); got != "prior" {
		t.Errorf("要記下重複的對象，實得 %q", got)
	}
}

// 這是整個機制存在的理由：feed 是截斷的，同一個賣家用模板發的兩則不同商品
// （手鑽 vs 神之手剪鉗）feed 前綴一模一樣。只憑 feed 判就會誤擋。
func TestFeedMatchWithDifferentContentIsSent(t *testing.T) {
	c := []dupCandidate{{ID: "prior", ContentKey: "OTHER", BodyFinal: true}}
	if v := decideDup(pend("C", true), c); v != DupSend {
		t.Errorf("完整內文不同就該送，實得 %v", v)
	}
}

// 自己的內文還沒補到 → 先留著，不要用 feed 判死
func TestHoldsWhenOwnBodyNotFinal(t *testing.T) {
	c := []dupCandidate{{ID: "prior", ContentKey: "C", BodyFinal: true}}
	if v := decideDup(pend("", false), c); v != DupHold {
		t.Errorf("自己內文還沒定案該先留著，實得 %v", v)
	}
}

// 對方的內文還沒補到 → 一樣先留著
func TestHoldsWhenCandidateBodyNotFinal(t *testing.T) {
	c := []dupCandidate{{ID: "prior", ContentKey: "", BodyFinal: false}}
	if v := decideDup(pend("C", true), c); v != DupHold {
		t.Errorf("對方內文還沒定案該先留著，實得 %v", v)
	}
}

// 兩邊都不會再變了就必須決斷，否則會永遠留在佇列裡佔額度。
// 這時候只有 feed 對上，證據不足 —— 寧可多送一則，不要吃掉可能不同的貼文。
func TestDecidesSendWhenBothFinalButOnlyFeedMatched(t *testing.T) {
	c := []dupCandidate{{ID: "prior", ContentKey: "", BodyFinal: true}}
	if v := decideDup(pend("", true), c); v != DupSend {
		t.Errorf("兩邊都定案又比不出來就該送，實得 %v", v)
	}
}

// 多個疑似對象裡只要有一個內文相同就算重複
func TestSuppressesWhenAnyCandidateMatches(t *testing.T) {
	c := []dupCandidate{
		{ID: "a", ContentKey: "X", BodyFinal: true},
		{ID: "b", ContentKey: "C", BodyFinal: true},
	}
	if v := decideDup(pend("C", true), c); v != DupSuppress {
		t.Errorf("實得 %v", v)
	}
	if got := pickDup(pend("C", true), c); got != "b" {
		t.Errorf("要指到內文相同的那一則，實得 %q", got)
	}
}

func TestContentKeyIgnoresWhitespaceOnly(t *testing.T) {
	a := ContentKey("", "SELL 出售\n1.限量版海報 A版 $1600")
	b := ContentKey("", "SELL 出售   1.限量版海報    A版 $1600")
	if a == "" || a != b {
		t.Fatalf("只差空白應得相同的鍵：%q vs %q", a, b)
	}
}

// 標點與 emoji 要算進去。實測有兩則只差一個 🥺，那是同一則被編輯過，
// 但「只差一個字就當成同一則」的規則太危險 —— 只差一個價格數字的也會被併掉。
func TestContentKeyKeepsPunctuationAndEmoji(t *testing.T) {
	if ContentKey("", "拜託拜託 誠徵請帶著價格") == ContentKey("", "拜託拜託🥺 誠徵請帶著價格") {
		t.Error("emoji 不同就該是不同的鍵")
	}
	if ContentKey("", "出售 海報 $1600") == ContentKey("", "出售 海報 $1500") {
		t.Error("只有價格不同也算不同內容 —— 降價重貼是值得知道的事")
	}
}

func TestContentKeySkipsVeryShortBodies(t *testing.T) {
	for _, s := range []string{"", "  ", "+1", "收", "有興趣", "完售了"} {
		if k := ContentKey("", s); k != "" {
			t.Errorf("ContentKey(\"\", %q) = %q，過短的內容不該產生鍵", s, k)
		}
	}
}

// 不同頻道的同樣內容是兩回事。不看目的地的話，跨社團去重會讓某個頻道
// 整個收不到 —— 而分流的理由本來就是「這些要送到別的地方」。
func TestDuplicateOfRespectsChannel(t *testing.T) {
	st, ctx := testStore(t)
	stamp := time.Now().UnixNano()
	gDefault := fmt.Sprintf("dedup-def-%d", stamp)
	gOther := fmt.Sprintf("dedup-oth-%d", stamp)

	for _, g := range []struct{ id, hook string }{{gDefault, ""}, {gOther, "HOOK_B"}} {
		if _, err := st.pool.Exec(ctx,
			`INSERT INTO groups (id, webhook_env) VALUES ($1, NULLIF($2,''))`, g.id, g.hook); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = st.pool.Exec(ctx, `DELETE FROM groups WHERE id = $1`, g.id) })
	}

	// 已經在預設頻道送出去的那一則
	prior := fmt.Sprintf("prior-%d", stamp)
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO listings (id, group_id, seller_id, status, first_seen, last_seen,
		                      parser_version, body_fetched, content_key, feed_key, notified_at)
		VALUES ($1,$2,'seller-x','selling',now(),now(),1,TRUE,'KEY','FEED',now())`,
		prior, gDefault); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(ctx, `DELETE FROM listings WHERE id = $1`, prior) })

	same := Pending{ID: "x1", SellerID: "seller-x", ContentKey: "KEY", FeedKey: "FEED",
		BodyFinal: true, WebhookEnv: ""}
	if v, of, err := st.DuplicateOf(ctx, same); err != nil || v != DupSuppress || of != prior {
		t.Errorf("同頻道同內容該擋下，實得 v=%v of=%q err=%v", v, of, err)
	}

	otherChannel := same
	otherChannel.WebhookEnv = "HOOK_B"
	if v, _, err := st.DuplicateOf(ctx, otherChannel); err != nil || v != DupSend {
		t.Errorf("不同頻道不該去重，否則那個頻道會整個收不到，實得 v=%v err=%v", v, err)
	}

	otherSeller := same
	otherSeller.SellerID = "seller-y"
	if v, _, err := st.DuplicateOf(ctx, otherSeller); err != nil || v != DupSend {
		t.Errorf("不同賣家不該去重，實得 v=%v err=%v", v, err)
	}
}

// 內文夠長的時候不看標題。同賣家、內文逐字相同但標題不同的實測有 14 組 /
// 31 則，幾乎都是同一件東西重貼 —— 打錯字、補上「已售出」、換個說法。
// 把標題算進去會讓這些全變成不同的鍵，等於少擋 17 則。
func TestContentKeyIgnoresTitleWhenBodyIsEnough(t *testing.T) {
	a := ContentKey("韓版專輯 未拆", "日本帶回，售1700含運，附小卡 台南面交可議")
	b := ContentKey("（已售出） 韓版專輯 未拆", "日本帶回，售1700含運，附小卡 台南面交可議")
	if a == "" {
		t.Fatal("內文夠長就該算得出鍵")
	}
	if a != b {
		t.Error("內文一樣、只有標題被編輯過，是同一則重貼")
	}
}

// 內文短到算不出鍵的，標題補上去要能救回來。
// 實測 1134 則有內文的貼文裡有 28 則（2.5%）內文不到 10 字，完全不參與比對。
func TestContentKeyRescuesShortBodiesWithTitle(t *testing.T) {
	if k := ContentKey("", "店到店運費60"); k != "" {
		t.Fatalf("前提變了：這段內文本來就該短到算不出鍵，實得 %q", k)
	}
	if k := ContentKey("限量版海報 附特典", "店到店運費60"); k == "" {
		t.Error("帶標題之後長度夠了，不該再是空鍵")
	}
}

// 借了標題還是不夠長的，照樣不給鍵。
func TestContentKeyShortEvenWithTitle(t *testing.T) {
	if k := ContentKey("收", "+1"); k != "" {
		t.Errorf("標題加內文仍然過短，不該產生鍵，實得 %q", k)
	}
}

// 借標題的那條路徑上，標題與內文之間不加分隔符號。
func TestContentKeyJoinsTitleAndBodyWhenBorrowing(t *testing.T) {
	if ContentKey("限量海報A款", "$1600") != ContentKey("限量海報A款 $1600", "") {
		t.Error("內文不夠長而借標題時，字怎麼拆不該影響結果")
	}
}
