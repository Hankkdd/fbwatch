// Package store 是唯一的真實來源。collector 只寫入，其他元件只讀取。
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"fbwatch/internal/parse"
)

// ParserVersion 在解析邏輯有語意變動時遞增，
// 讓之後能只對舊版本的列重跑解析，不必全量重算。
const ParserVersion = 1

// embed 不能引用上層目錄，所以 migrations 放在套件底下。
//
//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("連線 Postgres 失敗: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		b, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return nil
}

// Upsert 寫入一則 listing，回傳它是否為首次出現。
// 已存在的只更新 last_seen 與可能變動的欄位，first_seen 保持不動。
func (s *Store) Upsert(ctx context.Context, groupID string, l parse.Listing, now time.Time) (isNew bool, err error) {
	// 第二道防線。解析端已經要求必須有 ID，但寫入層不該相信呼叫端 ——
	// 沒有 permalink 的東西不是社團貼文，實測曾把 Messenger 的私人對話寫進來。
	if l.ID == "" {
		return false, fmt.Errorf("listing 缺少 ID，拒絕寫入")
	}

	price := nullableInt(l.Price)
	ntTag := nullableInt(l.NTTag)

	// feed 上的時間只有小時級精度（「1小時」可能實際是 1 小時 55 分），
	// 推算出來可能落在我們看到它之後 —— 實測出現過 -7 分鐘的 feed_lag。
	// 貼文不可能晚於我們第一次看到它，所以鉗制在 now。
	var postedAt *time.Time
	if !l.PostedAt.IsZero() {
		p := l.PostedAt
		if p.After(now) {
			p = now
		}
		postedAt = &p
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO listings (
			id, group_id, seller_id, body_feed, truncated,
			price, nt_tag, status, permalink, raw_html,
			posted_at, first_seen, last_seen, parser_version, title, feed_key
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET
			last_seen = EXCLUDED.last_seen,
			status    = EXCLUDED.status,
			price     = COALESCE(EXCLUDED.price, listings.price),
			body_feed = EXCLUDED.body_feed,
			raw_html  = EXCLUDED.raw_html,
			-- 卡片沒渲染完整的那一輪會抓不到標題，不要拿空值蓋掉之前抓到的
			title     = COALESCE(EXCLUDED.title, listings.title),
			-- 相對時間的精度只會隨時間變差（「25分鐘」之後會變成「1小時」），
			-- 所以第一次算出來的值最準，不要覆蓋。
			posted_at = COALESCE(listings.posted_at, EXCLUDED.posted_at),
			-- 第一眼的 feed 文字才有用：賣家編輯貼文、feed 上的相對時間變動，
			-- 都會讓後來的 body_feed 跟當初不同，鍵跟著變就對不上了。
			feed_key  = COALESCE(listings.feed_key, EXCLUDED.feed_key)
		RETURNING (xmax = 0)
	`,
		l.ID, groupID, nullableStr(l.SellerID), l.Text, l.Truncated,
		price, ntTag, string(l.Status), l.Permalink, l.RawHTML,
		postedAt, now, ParserVersion, nullableStr(l.Title),
		nullableStr(ContentKey(l.Title, l.Text)),
	).Scan(&isNew)
	if err != nil {
		return false, err
	}

	if l.Price > 0 {
		// IS DISTINCT FROM 同時涵蓋「沒有前一筆」與「價格有變動」兩種情況
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO price_history (listing_id, price, observed_at)
			SELECT $1, $2, $3
			WHERE $2 IS DISTINCT FROM (
				SELECT price FROM price_history
				WHERE listing_id = $1
				ORDER BY observed_at DESC LIMIT 1
			)
			ON CONFLICT DO NOTHING
		`, l.ID, l.Price, now); err != nil {
			return isNew, err
		}
	}
	return isNew, nil
}

// maxBodyAttempts 限制對同一則詳情頁的請求次數。
// 允許重試是因為網路瞬斷不該讓內文永久缺失，但必須有上限 ——
// 無上限的重試會在 FB 改版時變成對同一批網址的持續請求。
const maxBodyAttempts = 3

type Pending struct {
	ID         string
	GroupID    string
	Permalink  string
	Title      string // 商品標題，一般貼文為空
	Status     string // selling / sold / wanted，由內文推斷
	Body       string // 完整內文，尚未補抓時退回 feed 上的截斷版
	SellerName string
	Muted      bool // 靜音的賣家只送一行，不送全文
	// WebhookEnv 決定這則要送到哪個聊天室，空字串表示預設的那個。
	WebhookEnv string
	// BodyFinal 表示內文不會再變了（補抓成功，或重試次數已用盡）。
	// 過濾與去重都要看這個：還可能變豐富的時候不該判死。
	BodyFinal bool

	// 去重用。SellerID 是必要條件，FeedKey 只挑「疑似」，ContentKey 才是判準。
	SellerID   string
	FeedKey    string
	ContentKey string
}

// UpsertSeller 登記賣家。名稱之後可能才抓到，所以不覆蓋成空值。
func (s *Store) UpsertSeller(ctx context.Context, id, name string) error {
	if id == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sellers (id, name) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET name = COALESCE(NULLIF(EXCLUDED.name, ''), sellers.name)`,
		id, name)
	return err
}

// PendingBodies 回傳尚未抓過詳情頁、且重試次數未達上限的 listing。
//
// 不限於內文被截斷的：詳情頁同時帶精確的 creation_time，
// 而 feed 的相對時間只有小時級精度，延遲量測需要精確值才可信。
func (s *Store) PendingBodies(ctx context.Context, limit int) ([]Pending, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, group_id, permalink FROM listings
		WHERE NOT body_fetched AND body_attempts < $2
		ORDER BY first_seen DESC
		LIMIT $1`, limit, maxBodyAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.GroupID, &p.Permalink); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateBody 寫入詳情頁取得的完整內文與結構化欄位。
//
// 價格與狀態用完整內文重算 —— feed 的截斷版可能讓價格抓錯。
// creation_time 是精確時間，一律覆蓋相對時間的推算值。
func (s *Store) UpdateBody(ctx context.Context, id string, d parse.Detail) error {
	// 沒有描述的貼文（只有照片）不重算價格與狀態，也不覆蓋既有內文，
	// 但仍要標記已抓過並記下精確時間，否則會一直重試到次數上限。
	var price *int
	var status *string
	if d.Description != "" {
		price = nullableInt(parse.ExtractPrice(d.Description))
		if price == nil && d.Price > 0 && d.Price != parse.WantedSentinel {
			price = &d.Price
		}
		st := string(parse.DetectStatus(d.Description, d.Price))
		status = &st
	}

	var createdAt *time.Time
	if !d.CreatedAt.IsZero() {
		createdAt = &d.CreatedAt
	}

	// 標題要一起算進鍵裡，從資料庫讀而不是由呼叫端傳：標題在 upsert 就寫好了，
	// 多一個參數就多一個忘記帶的機會，而漏帶只會讓鍵變弱，不會報錯。
	var title string
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(title, '') FROM listings WHERE id = $1`, id).Scan(&title); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE listings SET
			body         = COALESCE(NULLIF($2, ''), body),
			body_fetched = TRUE,
			price        = COALESCE($3, price),
			status       = COALESCE($4, status),
			location     = COALESCE($5, location),
			currency     = COALESCE($6, currency),
			posted_at       = COALESCE($7, posted_at),
			-- 只會由估算升級為精確，不會反向降級
			posted_at_exact = (posted_at_exact OR $7 IS NOT NULL),
			-- 去重的判準，由標題與完整內文一起算出。要等完整內文 —— feed 版是截斷的，
			-- 同一個賣家用模板發的兩則不同商品，feed 前綴會一模一樣。
			content_key  = COALESCE($8, content_key)
		WHERE id = $1`,
		id, d.Description, price, status,
		nullableStr(d.Location), nullableStr(d.Currency), createdAt,
		nullableStr(ContentKey(title, d.Description)))
	return err
}

// RecordBodyAttempt 記錄一次抓取嘗試，無論成敗。
// 先記再抓：抓取中途崩潰不該讓這則永遠重試。
func (s *Store) RecordBodyAttempt(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE listings SET body_attempts = body_attempts + 1 WHERE id = $1`, id)
	return err
}

// PendingNotifications 回傳尚未通知的 listing，舊的優先。
//
// skipWebhookEnv 列出當下送不出去的通知管道，整個排除在佇列之外。
// 不排除的話，一個設錯 webhook 的社團會用送不出去的貼文塞滿這 20 則額度，
// 其他社團的通知就跟著一起停掉 —— 一個設定錯誤變成全站故障。
func (s *Store) PendingNotifications(ctx context.Context, limit int, skipWebhookEnv []string) ([]Pending, error) {
	// 一定要是非 nil 的空切片。nil 會被當成 SQL 的 NULL，而 `x = ANY(NULL)`
	// 的結果是 NULL 不是 false，NOT NULL 還是 NULL，WHERE 就把每一列都濾掉了 ——
	// 沒有壞掉的管道時（也就是絕大多數時候）變成一則通知都不送，而且完全不報錯。
	if skipWebhookEnv == nil {
		skipWebhookEnv = []string{}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT l.id, l.group_id, l.permalink, COALESCE(l.title, ''), COALESCE(l.status, ''), COALESCE(l.body, l.body_feed, ''),
		       COALESCE(NULLIF(s.label,''), s.name, ''), COALESCE(s.muted, FALSE),
		       COALESCE(g.webhook_env, ''),
		       (l.body_fetched OR l.body_attempts >= $3),
		       COALESCE(l.seller_id, ''), COALESCE(l.feed_key, ''), COALESCE(l.content_key, '')
		FROM listings l
		LEFT JOIN sellers s ON s.id = l.seller_id
		LEFT JOIN groups g ON g.id = l.group_id
		WHERE l.notified_at IS NULL
		  AND NOT (COALESCE(g.webhook_env, '') = ANY($2))
		ORDER BY l.first_seen ASC
		LIMIT $1`, limit, skipWebhookEnv, maxBodyAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.GroupID, &p.Permalink, &p.Title, &p.Status, &p.Body,
			&p.SellerName, &p.Muted, &p.WebhookEnv, &p.BodyFinal,
			&p.SellerID, &p.FeedKey, &p.ContentKey); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkNotified 標記已送出通知。首次接觸一個社團時，
// 把當下抓到的全部標成已通知，避免第一次執行就對既有貼文發推播。
func (s *Store) MarkNotified(ctx context.Context, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE listings SET notified_at = $2 WHERE id = ANY($1) AND notified_at IS NULL`,
		ids, now)
	return err
}

// GroupSeeded 回報某社團是否已有資料。用來決定首次執行要不要發通知。
func (s *Store) GroupSeeded(ctx context.Context, groupID string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM listings WHERE group_id = $1`, groupID).Scan(&n)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	return n > 0, nil
}

type Group struct {
	ID   string
	Name string
	// WebhookEnv 是存放這個社團 webhook 的環境變數名稱，空字串表示用預設的。
	WebhookEnv string
	// RequirePatterns 是只通知命中其一的貼文，空表示不過濾。
	RequirePatterns []string
}

// EnabledGroups 回傳目前要監控的社團。每輪都重讀，
// 所以在資料庫改 enabled 即時生效，不必重啟。
func (s *Store) EnabledGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, COALESCE(name, ''), COALESCE(webhook_env, ''),
		        COALESCE(require_patterns, '{}')
		 FROM groups WHERE enabled ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.WebhookEnv, &g.RequirePatterns); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SHA256ForPhoto 查某張 FB 照片是否已經下載過。
//
// 在下載前就去重：跨社團轉貼的同一則商品會重複出現同樣的 photo_id，
// 沒必要為了算出一樣的雜湊再下載一次。
func (s *Store) SHA256ForPhoto(ctx context.Context, photoID string) (string, bool, error) {
	if photoID == "" {
		return "", false, nil
	}
	var sum string
	err := s.pool.QueryRow(ctx,
		`SELECT sha256 FROM listing_images
		 WHERE photo_id = $1 AND sha256 IS NOT NULL LIMIT 1`, photoID).Scan(&sum)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return sum, true, nil
}

// RecordImage 登記一張已存進物件儲存的圖片。
func (s *Store) RecordImage(ctx context.Context, sha256, objectKey string, size int, contentType string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO images (sha256, object_key, bytes, content_type)
		VALUES ($1,$2,$3,$4) ON CONFLICT (sha256) DO NOTHING`,
		sha256, objectKey, size, nullableStr(contentType))
	return err
}

// LinkImage 把圖片掛到 listing 上。sha256 為空表示下載失敗，
// 仍記錄一列以保留順序與 cdn_url，供日後查證。
func (s *Store) LinkImage(ctx context.Context, listingID string, idx int, photoID, sha256, cdnURL, caption string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO listing_images (listing_id, idx, photo_id, sha256, cdn_url, caption)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (listing_id, idx) DO UPDATE SET
			photo_id = EXCLUDED.photo_id,
			sha256   = COALESCE(EXCLUDED.sha256, listing_images.sha256),
			cdn_url  = EXCLUDED.cdn_url,
			caption  = EXCLUDED.caption`,
		listingID, idx, nullableStr(photoID), nullableStr(sha256),
		nullableStr(cdnURL), nullableStr(caption))
	return err
}

func (s *Store) MarkImagesFetched(ctx context.Context, listingID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE listings SET images_fetched = TRUE WHERE id = $1`, listingID)
	return err
}

// LastNewItem 回傳最後一次出現新 listing 的時間。
// 第二個回傳值為 false 表示資料庫還是空的。
func (s *Store) LastNewItem(ctx context.Context) (time.Time, bool, error) {
	var t *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(first_seen) FROM listings`).Scan(&t); err != nil {
		return time.Time{}, false, err
	}
	if t == nil {
		return time.Time{}, false, nil
	}
	return *t, true, nil
}

func (s *Store) RecordPoll(ctx context.Context, groupID string, ranAt time.Time, parsed, newItems int, dur time.Duration, runErr error) error {
	var errStr *string
	if runErr != nil {
		e := runErr.Error()
		errStr = &e
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO poll_runs (group_id, ran_at, parsed, new_items, duration_ms, error)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, groupID, ranAt, parsed, newItems, dur.Milliseconds(), errStr)
	return err
}

// nullableInt 把解析不到或不合理的數值寫成 NULL。
//
// 上界是最後一道防線，不是在做資料清理：解析器永遠會有沒想到的輸入，
// 但一個壞掉的數字不該把整個 collector 弄停機。2026-10-16 就是這樣 ——
// 貼文裡一串 2610168104526 被當成價格，超過 int4 上限，寫入連續失敗
// 觸發熔斷，半小時沒有監控。寧可這一欄是 NULL：body 與 raw_html 都留著，
// parser_version 也在，解析器改好之後可以回頭重算。
func nullableInt(v int) *int {
	if v <= 0 || v > math.MaxInt32 {
		return nil
	}
	return &v
}

func nullableStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// isNoRows 判斷查詢是否沒有結果。
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// RawHTMLRow 是回填用的一筆原始 HTML。
type RawHTMLRow struct {
	ID      string
	RawHTML string
	Title   string // 目前存的標題，空字串表示沒有
}

// RawHTMLAfter 依 id 分頁讀出原始 HTML，回填工具用。
// raw_html 一筆幾十 KB，一次全讀會吃掉上百 MB，所以分頁。
func (s *Store) RawHTMLAfter(ctx context.Context, afterID string, limit int) ([]RawHTMLRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(raw_html, ''), COALESCE(title, '') FROM listings
		WHERE id > $1 ORDER BY id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RawHTMLRow
	for rows.Next() {
		var r RawHTMLRow
		if err := rows.Scan(&r.ID, &r.RawHTML, &r.Title); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetTitle 寫入回填出來的標題。
func (s *Store) SetTitle(ctx context.Context, id, title string) error {
	_, err := s.pool.Exec(ctx, `UPDATE listings SET title = $2 WHERE id = $1`, id, nullableStr(title))
	return err
}

// EnableMCPLogin 替唯讀角色 fbwatch_mcp 開啟登入並設定密碼。
//
// ALTER ROLE 不吃參數綁定，所以先用 format(%L) 在資料庫端做字串跳脫，再執行產生的語句。
func (s *Store) EnableMCPLogin(ctx context.Context, password string) error {
	var stmt string
	if err := s.pool.QueryRow(ctx,
		`SELECT format('ALTER ROLE fbwatch_mcp LOGIN PASSWORD %L', $1::text)`, password,
	).Scan(&stmt); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, stmt)
	return err
}

// ErrNotFound 表示資料庫裡沒有這筆。
var ErrNotFound = errors.New("not found")

// Permalink 回傳貼文的網址。留言查詢用：呼叫端只能給 ID，網址一律從這裡來，
// 這樣比價 bot 沒辦法叫瀏覽器去開任意網頁。
func (s *Store) Permalink(ctx context.Context, id string) (string, error) {
	var p string
	err := s.pool.QueryRow(ctx, `SELECT permalink FROM listings WHERE id = $1`, id).Scan(&p)
	if isNoRows(err) {
		return "", ErrNotFound
	}
	return p, err
}
