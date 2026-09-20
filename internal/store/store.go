// Package store 是唯一的真實來源。collector 只寫入，其他元件只讀取。
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
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
	price := nullableInt(l.Price)
	ntTag := nullableInt(l.NTTag)

	var postedAt *time.Time
	if !l.PostedAt.IsZero() {
		postedAt = &l.PostedAt
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO listings (
			id, group_id, seller_id, body_feed, truncated,
			price, nt_tag, status, permalink, raw_html,
			posted_at, first_seen, last_seen, parser_version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12,$13)
		ON CONFLICT (id) DO UPDATE SET
			last_seen = EXCLUDED.last_seen,
			status    = EXCLUDED.status,
			price     = COALESCE(EXCLUDED.price, listings.price),
			body_feed = EXCLUDED.body_feed,
			raw_html  = EXCLUDED.raw_html,
			-- 相對時間的精度只會隨時間變差（「25分鐘」之後會變成「1小時」），
			-- 所以第一次算出來的值最準，不要覆蓋。
			posted_at = COALESCE(listings.posted_at, EXCLUDED.posted_at)
		RETURNING (xmax = 0)
	`,
		l.ID, groupID, nullableStr(l.SellerID), l.Text, l.Truncated,
		price, ntTag, string(l.Status), l.Permalink, l.RawHTML,
		postedAt, now, ParserVersion,
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
	ID        string
	GroupID   string
	Permalink string
}

// PendingBodies 回傳內文被截斷、尚未取得完整版、且重試次數未達上限的 listing。
func (s *Store) PendingBodies(ctx context.Context, limit int) ([]Pending, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, group_id, permalink FROM listings
		WHERE truncated AND NOT body_fetched AND body_attempts < $2
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
	price := nullableInt(parse.ExtractPrice(d.Description))
	if price == nil && d.Price > 0 && d.Price != parse.WantedSentinel {
		price = &d.Price
	}
	status := string(parse.DetectStatus(d.Description, d.Price))

	var createdAt *time.Time
	if !d.CreatedAt.IsZero() {
		createdAt = &d.CreatedAt
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE listings SET
			body         = $2,
			body_fetched = TRUE,
			price        = COALESCE($3, price),
			status       = $4,
			location     = COALESCE($5, location),
			currency     = COALESCE($6, currency),
			posted_at       = COALESCE($7, posted_at),
			posted_at_exact = ($7 IS NOT NULL)
		WHERE id = $1`,
		id, d.Description, price, status,
		nullableStr(d.Location), nullableStr(d.Currency), createdAt)
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
func (s *Store) PendingNotifications(ctx context.Context, limit int) ([]Pending, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, group_id, permalink FROM listings
		WHERE notified_at IS NULL
		ORDER BY first_seen ASC
		LIMIT $1`, limit)
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

func nullableInt(v int) *int {
	if v <= 0 {
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
