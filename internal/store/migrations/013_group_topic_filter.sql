-- 社團可以只通知命中特定詞的貼文。
--
-- 「模型工具」那種社團裡什麼都有：工具、塗料、模型本體、還有人貼政治新聞。
-- 每一則都推等於沒在篩。NULL 或空陣列表示不過濾（維持原本行為）——
-- 設定從缺應該退回原本的樣子，不是把整個社團靜音。
ALTER TABLE groups ADD COLUMN IF NOT EXISTS require_patterns TEXT[];
