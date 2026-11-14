package store

import "context"

// DupVerdict 是去重的判斷結果。
type DupVerdict int

const (
	// DupSend：不是重複，送出。
	DupSend DupVerdict = iota
	// DupSuppress：確定是重複，擋掉。
	DupSuppress
	// DupHold：有疑似對象但還判不準，先留著，下一輪內文補到了再判。
	//
	// 這個狀態是整個機制的重點。feed 文字是截斷的，用它判會誤擋 ——
	// 實測有賣家用模板發文，兩則不同商品（手鑽 vs 神之手剪鉗）的 feed 前綴
	// 一模一樣。所以 feed 只用來挑出「疑似」，判準一律等完整內文。
	//
	// 延遲只落在疑似重複的那一則身上，前一則早就送出去了 ——
	// 成本放在很可能冗餘的那一份上，不影響你第一時間看到東西。
	DupHold
)

// dupCandidate 是一個疑似對象。
type dupCandidate struct {
	ID         string
	ContentKey string
	BodyFinal  bool
}

// DuplicateOf 判斷這則要不要因為重複而擋下，並回傳重複的對象 id。
//
// 三個條件缺一不可：
//
//   - 同一個賣家。只比內容的話，兩個不同的人貼一樣的短句就會被誤判。
//     實測有 4 組不同賣家內容相同。
//   - 同一個通知頻道。不看目的地的話，跨社團去重會讓某個頻道整個收不到 ——
//     分流的理由本來就是「這些要送到別的地方」。
//   - 對方已經通知過。還沒送出去的那一則由批次內的記憶體集合處理。
func (s *Store) DuplicateOf(ctx context.Context, p Pending) (DupVerdict, string, error) {
	if p.SellerID == "" || (p.FeedKey == "" && p.ContentKey == "") {
		return DupSend, "", nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT l.id, COALESCE(l.content_key, ''),
		       (l.body_fetched OR l.body_attempts >= $5)
		FROM listings l
		LEFT JOIN groups g ON g.id = l.group_id
		WHERE l.seller_id = $1
		  AND l.id <> $2
		  AND l.notified_at IS NOT NULL
		  AND l.duplicate_of IS NULL
		  AND COALESCE(g.webhook_env, '') = $3
		  AND (
		        (NULLIF($4,'') IS NOT NULL AND l.content_key = $4)
		     OR (NULLIF($6,'') IS NOT NULL AND l.feed_key    = $6)
		  )
		LIMIT 20`,
		p.SellerID, p.ID, p.WebhookEnv, p.ContentKey, maxBodyAttempts, p.FeedKey)
	if err != nil {
		return DupSend, "", err
	}
	defer rows.Close()

	var cands []dupCandidate
	for rows.Next() {
		var c dupCandidate
		if err := rows.Scan(&c.ID, &c.ContentKey, &c.BodyFinal); err != nil {
			return DupSend, "", err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return DupSend, "", err
	}
	return decideDup(p, cands), pickDup(p, cands), nil
}

// decideDup 是判斷本身，跟資料庫分開才測得動。
func decideDup(p Pending, cands []dupCandidate) DupVerdict {
	if len(cands) == 0 {
		return DupSend
	}
	// 完整內文相同才算數
	if p.ContentKey != "" {
		for _, c := range cands {
			if c.ContentKey == p.ContentKey {
				return DupSuppress
			}
		}
	}
	// 走到這裡表示只有 feed 對上，或有一邊的完整內文還沒拿到。
	// 只要還有機會拿到更好的依據就先留著；兩邊都不會再變了才放行。
	for _, c := range cands {
		if !c.BodyFinal || !p.BodyFinal {
			return DupHold
		}
	}
	return DupSend
}

// pickDup 回傳要記在 duplicate_of 的對象，只有 DupSuppress 時有意義。
func pickDup(p Pending, cands []dupCandidate) string {
	if p.ContentKey == "" {
		return ""
	}
	for _, c := range cands {
		if c.ContentKey == p.ContentKey {
			return c.ID
		}
	}
	return ""
}

// MarkDuplicate 記下這則是誰的重複，並標成已處理。
//
// 一定要標成已處理：留在佇列裡會一直重判，佔滿 20 則的額度，
// 最後連該通知的也擠不進來。
func (s *Store) MarkDuplicate(ctx context.Context, id, ofID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE listings SET duplicate_of = $2, notified_at = now()
		WHERE id = $1 AND notified_at IS NULL`, id, ofID)
	return err
}
