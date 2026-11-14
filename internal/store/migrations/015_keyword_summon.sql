-- 命中這個關鍵字時，在通知裡 tag 比價 bot，讓它自動比價。
--
-- 跟 enabled 分開：enabled 管「要不要加提示行」，summon 管「要不要叫 bot 出來」。
-- 叫 bot 要花模型額度，所以預設關。改這欄即時生效，不必重啟。
ALTER TABLE keywords ADD COLUMN IF NOT EXISTS summon BOOLEAN NOT NULL DEFAULT FALSE;
