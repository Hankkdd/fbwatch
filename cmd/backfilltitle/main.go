// backfilltitle 從已存的 raw_html 重新抽出商品標題，補進 title 欄位。
//
// 預設只統計不寫入，確認命中率合理再加 -write。
// 已經有標題的列不會被覆蓋成空值。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"golang.org/x/net/html"

	"fbwatch/internal/parse"
	"fbwatch/internal/store"
)

func main() {
	write := flag.Bool("write", false, "實際寫入資料庫；沒加的話只統計")
	samples := flag.Int("samples", 10, "印出幾筆抽到的標題當樣本")
	flag.Parse()

	dsn := os.Getenv("FBWATCH_DSN")
	if dsn == "" {
		log.Fatal("請設定 FBWATCH_DSN")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatal(err)
	}

	var total, found, changed, withNT, withNTFound int
	after := ""
	for {
		rows, err := st.RawHTMLAfter(ctx, after, 200)
		if err != nil {
			log.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			after = r.ID
			total++
			doc, err := html.Parse(strings.NewReader("<html><body>" + r.RawHTML + "</body></html>"))
			if err != nil {
				continue
			}
			t := parse.ExtractTitle(doc)
			hasNT := strings.Contains(r.RawHTML, "NT$")
			if hasNT {
				withNT++
			}
			if t == "" {
				continue
			}
			found++
			if hasNT {
				withNTFound++
			}
			if found <= *samples {
				fmt.Printf("  %s  %s\n", r.ID, t)
			}
			if t == r.Title {
				continue
			}
			changed++
			if *write {
				if err := st.SetTitle(ctx, r.ID, t); err != nil {
					log.Fatal(err)
				}
			}
		}
	}

	fmt.Printf("總筆數 %d；抽到標題 %d；有 NT$ 價格列 %d，其中抽到 %d；與現有值不同 %d\n",
		total, found, withNT, withNTFound, changed)
	if !*write {
		fmt.Println("沒有寫入（加 -write 才會寫）")
	}
}
