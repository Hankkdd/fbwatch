-- 監控中的社團。資料庫是唯一真實來源，改這張表即時生效，不必重啟或重建。
--
-- FBWATCH_GROUPS 只在這張表是空的時候用來初始化，之後一律以表為準 ——
-- 兩個來源同時有效只會讓人搞不清楚哪個說了算。
CREATE TABLE IF NOT EXISTS groups (
    id       TEXT PRIMARY KEY,
    name     TEXT,
    enabled  BOOLEAN     NOT NULL DEFAULT TRUE,
    note     TEXT,                       -- 停用原因等備註
    added_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS groups_enabled ON groups (enabled) WHERE enabled;
