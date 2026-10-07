package notify

import (
	"fmt"
	"strings"
)

// ShopEventMessage 把賣貨便的變動排成「稱呼　商品名稱」加網址兩行。
//
// 名稱後面只補最小限度的資訊：少了它，「新上架」與「完售」會長得一模一樣。
// 商品沒有各自的網址（整頁用彈窗呈現），所以連結只能指到賣場頁。
func ShopEventMessage(kind, shopName, itemName string, oldPrice, newPrice, oldQty, newQty int, shopURL string) string {
	if strings.TrimSpace(itemName) == "" {
		itemName = "（無名稱）"
	}
	if strings.TrimSpace(shopName) == "" {
		shopName = "賣場"
	}

	var tail string
	switch kind {
	case "new":
		tail = fmt.Sprintf("$%d", newPrice)
	case "price":
		arrow := "↓"
		if newPrice > oldPrice {
			arrow = "↑"
		}
		tail = fmt.Sprintf("$%d %s $%d", oldPrice, arrow, newPrice)
	case "restock":
		tail = fmt.Sprintf("補貨 $%d", newPrice)
	case "soldout":
		tail = "完售"
	default:
		tail = kind
	}

	return fmt.Sprintf("%s　%s　%s\n<%s>", shopName, itemName, tail, shopURL)
}
