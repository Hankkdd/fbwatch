package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func capture(t *testing.T, send func(d *Discord) error) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := send(NewDiscord(srv.URL)); err != nil {
		t.Fatal(err)
	}
	return got
}

// 貼文內文寫了 @everyone 也不能真的通知全伺服器。
func TestSendNeverPingsByDefault(t *testing.T) {
	got := capture(t, func(d *Discord) error { return d.Send(context.Background(), "@everyone 便宜賣") })
	am, _ := got["allowed_mentions"].(map[string]any)
	if am == nil {
		t.Fatal("一定要帶 allowed_mentions，不然 Discord 會照 content 解析")
	}
	if parse, _ := am["parse"].([]any); len(parse) != 0 {
		t.Errorf("parse 要是空的，得到 %v", parse)
	}
	if _, ok := am["users"]; ok {
		t.Errorf("沒指定時不該放行任何人：%v", am)
	}
}

func TestSendMentioningAllowsOnlyThatUser(t *testing.T) {
	got := capture(t, func(d *Discord) error {
		return d.SendMentioning(context.Background(), "售 紅字 <@42> <@99>", "42")
	})
	users, _ := got["allowed_mentions"].(map[string]any)["users"].([]any)
	if len(users) != 1 || users[0] != "42" {
		t.Errorf("只能放行 42，得到 %v", users)
	}
}
