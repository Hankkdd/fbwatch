// Package seed 讀「要追什麼」的設定檔。
//
// 這些內容以前寫在 migration 的 INSERT 裡。那是錯的位置：migration 會在每個
// 人的資料庫上跑，等於把作者的偏好裝到所有人機器上。migration 只管 schema，
// 內容交給這個檔案。
//
// 跟 .env 的分工：.env 放密鑰與節奏，這裡放「要追哪些社團、在意哪些字、
// 盯哪些賣場」。
package seed

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

type File struct {
	Groups   []Group   `yaml:"groups"`
	Keywords []Keyword `yaml:"keywords"`
	Shops    []Shop    `yaml:"shops"`
}

type Group struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	// WebhookEnv 是存放這個社團 webhook 的環境變數名稱，空字串表示用預設的。
	// 這裡只放名稱不放網址 —— 設定檔會跟著 pg_dump 之類的東西到處跑。
	WebhookEnv string `yaml:"webhook_env"`
	// RequirePatterns 是只通知命中其一的貼文，空表示不過濾。
	RequirePatterns []string `yaml:"require_patterns"`
}

type Keyword struct {
	Label    string   `yaml:"label"`
	Line     string   `yaml:"line"`
	Priority int      `yaml:"priority"`
	Patterns []string `yaml:"patterns"`
	Summon   bool     `yaml:"summon"`
}

type Shop struct {
	ID string `yaml:"id"`
	// Name 是你自己怎麼叫這個賣場，存進 shops.label。
	// 賣場在來源上的顯示名稱存在 shops.name，會被來源覆蓋，不是同一回事。
	Name string `yaml:"name"`
	Note string `yaml:"note"`
}

func (f File) Empty() bool {
	return len(f.Groups) == 0 && len(f.Keywords) == 0 && len(f.Shops) == 0
}

// Load 讀設定檔。檔案不存在不是錯誤 —— 全部在資料庫裡設定也是正常用法。
//
// 但內容有問題就要當場停下來。一個漏填 id 的社團如果被安靜地跳過，
// 症狀會是「什麼都沒通知」，而那離成因非常遠。
func Load(path string) (File, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}

	var f File
	// KnownFields：把打錯的欄位名當成錯誤。寫成 require_pattern 少一個 s
	// 而設定被默默忽略，是最難查的那種問題。
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	switch err := dec.Decode(&f); {
	case errors.Is(err, io.EOF): // 空檔案
		return File{}, nil
	case err != nil:
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.validate(); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (f File) validate() error {
	for i, g := range f.Groups {
		if strings.TrimSpace(g.ID) == "" {
			return fmt.Errorf("groups[%d] 缺少 id", i)
		}
	}
	for i, k := range f.Keywords {
		if strings.TrimSpace(k.Label) == "" {
			return fmt.Errorf("keywords[%d] 缺少 label", i)
		}
		if len(k.Patterns) == 0 {
			return fmt.Errorf("keywords[%q] 的 patterns 是空的，不會命中任何東西", k.Label)
		}
	}
	for i, s := range f.Shops {
		if strings.TrimSpace(s.ID) == "" {
			return fmt.Errorf("shops[%d] 缺少 id", i)
		}
	}
	return nil
}
