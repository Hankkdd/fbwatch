CREATE TABLE IF NOT EXISTS listings (
    id             TEXT PRIMARY KEY,           -- commerce listing id
    group_id       TEXT        NOT NULL,
    seller_id      TEXT,

    body_feed      TEXT,                       -- feed 上的內文，可能被截斷
    body           TEXT,                       -- 完整內文，Stage 2b 回填
    truncated      BOOLEAN     NOT NULL DEFAULT FALSE,
    body_fetched   BOOLEAN     NOT NULL DEFAULT FALSE,

    price          INTEGER,                    -- 由內文判定，NULL 表示抓不到
    nt_tag         INTEGER,                    -- 結構化 NT$ 欄位，不可信
    status         TEXT        NOT NULL,       -- selling|wanted|sold|reserved

    permalink      TEXT,
    raw_html       TEXT,                       -- 容器 HTML，供解析器改版後重跑

    first_seen     TIMESTAMPTZ NOT NULL,
    last_seen      TIMESTAMPTZ NOT NULL,
    notified_at    TIMESTAMPTZ,
    parser_version INTEGER     NOT NULL
);

CREATE INDEX IF NOT EXISTS listings_group_first_seen ON listings (group_id, first_seen DESC);
CREATE INDEX IF NOT EXISTS listings_status          ON listings (status);
CREATE INDEX IF NOT EXISTS listings_pending_notify  ON listings (notified_at) WHERE notified_at IS NULL;
CREATE INDEX IF NOT EXISTS listings_pending_body    ON listings (body_fetched) WHERE body_fetched = FALSE;

-- 賣家改價是比價的訊號，獨立成表
CREATE TABLE IF NOT EXISTS price_history (
    listing_id  TEXT        NOT NULL REFERENCES listings(id) ON DELETE CASCADE,
    price       INTEGER     NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (listing_id, observed_at)
);

-- 每次輪詢的結果，用來偵測靜默失效（抓得到頁面但解析永遠是 0 則）
CREATE TABLE IF NOT EXISTS poll_runs (
    id          BIGSERIAL PRIMARY KEY,
    group_id    TEXT        NOT NULL,
    ran_at      TIMESTAMPTZ NOT NULL,
    parsed      INTEGER     NOT NULL,
    new_items   INTEGER     NOT NULL,
    duration_ms INTEGER     NOT NULL,
    error       TEXT
);

CREATE INDEX IF NOT EXISTS poll_runs_ran_at ON poll_runs (ran_at DESC);
