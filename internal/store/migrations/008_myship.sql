-- 7-11 賣貨便賣場。與 FB 社團是完全不同的來源：純 HTTP、不需登入、
-- 不需瀏覽器，所以沒有 session、熔斷、帳號風險那一整套。
--
-- 另外開表而不是塞進 listings：賣貨便的資料形狀不同（有規格與庫存、
-- 沒有單品網址），硬塞進 FB 形狀的表只會讓兩邊都難讀。
CREATE TABLE IF NOT EXISTS shops (
    id       TEXT PRIMARY KEY,          -- GM2606261732575
    name     TEXT,
    enabled  BOOLEAN     NOT NULL DEFAULT TRUE,
    note     TEXT,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 目前狀態。每輪抓完就覆蓋，用來跟下一輪比對出變動。
CREATE TABLE IF NOT EXISTS shop_items (
    shop_id    TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    name       TEXT,
    description TEXT,
    price      INTEGER,                 -- 最低規格價
    inventory  INTEGER,
    status     TEXT,
    image      TEXT,
    created_at TIMESTAMPTZ,             -- 賣貨便給的上架時間，精確
    first_seen TIMESTAMPTZ NOT NULL,
    last_seen  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (shop_id, item_id)
);

-- 變動事件，待通知。與目前狀態分開，才不會因為覆蓋狀態而遺失變動。
CREATE TABLE IF NOT EXISTS shop_events (
    id          BIGSERIAL PRIMARY KEY,
    shop_id     TEXT NOT NULL,
    item_id     TEXT NOT NULL,
    kind        TEXT NOT NULL,          -- new | price | restock | soldout
    old_price   INTEGER,
    new_price   INTEGER,
    old_qty     INTEGER,
    new_qty     INTEGER,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    notified_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS shop_events_pending ON shop_events (detected_at)
    WHERE notified_at IS NULL;
CREATE INDEX IF NOT EXISTS shop_items_created ON shop_items (created_at DESC);

-- label 是自訂稱呼，優先於賣場原名。
-- 與 sellers 同樣的理由：name 會被來源的顯示名稱覆蓋，自己要的叫法得分開存。
ALTER TABLE shops ADD COLUMN IF NOT EXISTS label TEXT;
