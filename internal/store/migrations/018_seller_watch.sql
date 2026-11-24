-- 盯人。keywords 盯的是主題，這兩欄盯的是人：有些賣家不管賣什麼都值得看一眼。
--
-- 為什麼不把賣家名字塞進 keywords 當 pattern：
--   keywords 比對的是標題與內文，不是作者。要讓它比對作者就得把作者名字
--   併進比對字串，於是每一條主題規則也跟著去比對人名 —— 哪天有人取的名字
--   剛好像某個主題，就會天天誤報，還自動叫比價 bot。
--   反方向也一樣錯：別人貼文裡提到這個賣家（「跟某某買過」）會被當成他本人在發。
--   盯人跟盯主題是兩件事，分開存。
--
-- 提示行直接用 label（你給他的稱呼），不另外存一句話：
-- 盯人要知道的就是「是誰」，keywords 那種 label + line 的兩段式在這裡沒有東西可填。
ALTER TABLE sellers ADD COLUMN IF NOT EXISTS watched BOOLEAN NOT NULL DEFAULT FALSE;

-- 命中時 tag 比價 bot。跟 keywords.summon 分開的理由相同：
-- 想看他發什麼，不代表每一則都要燒模型額度。
ALTER TABLE sellers ADD COLUMN IF NOT EXISTS summon BOOLEAN NOT NULL DEFAULT FALSE;

-- muted 與 watched 是互相矛盾的設定（一個要只剩一行，一個要更醒目）。
-- 與其在程式裡定誰贏，不如讓資料庫在寫入當下就拒絕 —— 這種設定錯誤
-- 在執行期只會表現成「怎麼沒跳」，事後很難追。
ALTER TABLE sellers DROP CONSTRAINT IF EXISTS sellers_not_muted_and_watched;
ALTER TABLE sellers ADD CONSTRAINT sellers_not_muted_and_watched CHECK (NOT (muted AND watched));
