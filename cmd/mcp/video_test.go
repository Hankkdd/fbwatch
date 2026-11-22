package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type transcriptPage struct {
	offset, next, total int
	hasMore             bool
	body                string
}

func readTranscriptPage(t *testing.T, out string) transcriptPage {
	t.Helper()
	if !utf8.ValidString(out) || len(out) >= 12_000 {
		t.Fatalf("page must be valid UTF-8 and stay below the smallest tool budget: %d bytes", len(out))
	}
	header, rest, ok := strings.Cut(out, "\n-----\n")
	if !ok {
		t.Fatal("missing transcript boundary")
	}
	var p transcriptPage
	found := false
	for line := range strings.SplitSeq(header, "\n") {
		if n, err := fmt.Sscanf(line, "offset=%d next_offset=%d total_chars=%d has_more=%t", &p.offset, &p.next, &p.total, &p.hasMore); n == 4 && err == nil {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("continuation metadata must appear before the transcript")
	}
	var closed bool
	p.body, closed = strings.CutSuffix(rest, "\n-----\n")
	if !closed {
		t.Fatal("missing closing transcript boundary")
	}
	return p
}

func TestTranscriptPagesPreserveEntireSource(t *testing.T) {
	for name, source := range map[string]string{
		"beyond previous cap": strings.Repeat("[45:18] Future releases.\n\n", 6_000) + "[1:13:00] end",
		"unicode long line":   strings.Repeat("千子😀e\u0301", 12_000),
		"short":               "[0:00] hello\n\n[0:31] goodbye",
		"exact page":          strings.Repeat("x", maxTranscriptPageBytes),
		"one over page":       strings.Repeat("x", maxTranscriptPageBytes+1),
		"empty":               "",
	} {
		t.Run(name, func(t *testing.T) {
			var rebuilt strings.Builder
			offset := 0
			for {
				out, err := formatTranscript("Video", "https://youtu.be/dQw4w9WgXcQ", source, offset)
				if err != nil {
					t.Fatal(err)
				}
				p := readTranscriptPage(t, out)
				if p.offset != offset || p.total != utf8.RuneCountInString(source) || p.next != offset+utf8.RuneCountInString(p.body) {
					t.Fatalf("incorrect page coordinates: %+v", p)
				}
				rebuilt.WriteString(p.body)
				if !p.hasMore {
					if p.next != p.total {
						t.Fatal("completion reported before the end")
					}
					break
				}
				if p.next <= offset || p.next >= p.total {
					t.Fatalf("continuation does not advance: %d -> %d", offset, p.next)
				}
				offset = p.next
			}
			if rebuilt.String() != source {
				t.Fatal("pagination lost, duplicated or changed source text")
			}
		})
	}
}

func TestTranscriptPagePrefersParagraphBoundary(t *testing.T) {
	first := "[0:00] " + strings.Repeat("a", 6_000) + "\n\n"
	source := first + "[0:31] " + strings.Repeat("b", 4_000)
	out, err := formatTranscript("Video", "Link", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := readTranscriptPage(t, out)
	if p.body != first || !p.hasMore {
		t.Fatal("page should end before the next timestamped paragraph")
	}
}

func TestTranscriptPageValidatesOffsetsAndBoundsTitle(t *testing.T) {
	for _, offset := range []int{-1, 4} {
		if _, err := formatTranscript("Video", "Link", "千😀子", offset); err == nil {
			t.Errorf("accepted out-of-range offset %d", offset)
		}
	}
	out, err := formatTranscript(strings.Repeat("😀", 100_000), "Link", "千😀子", 3)
	if err != nil {
		t.Fatal(err)
	}
	p := readTranscriptPage(t, out)
	if p.hasMore || p.body != "" || p.next != 3 {
		t.Fatal("offset at EOF should return an empty final page")
	}
	// Invalid input is rejected before any attempt to contact Hub.
	res, _, err := (&tools{}).video(context.Background(), nil, videoIn{URL: "https://youtu.be/dQw4w9WgXcQ", Offset: -1})
	if err != nil || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "offset") {
		t.Fatalf("negative offset: result=%v err=%v", res, err)
	}
}

func TestVideoPaginationReusesCompletedHubTask(t *testing.T) {
	source := strings.Repeat("[45:18] 千子與後半段😀\n\n", 5_000) + "[1:13:00] end"
	created := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hub-token" {
			t.Error("missing Hub authorization")
		}
		switch {
		case r.URL.Path == "/api/tasks" && r.Method == http.MethodPost:
			created++
			fmt.Fprint(w, `{"id":"t1","title":"Video","status":"idle","activity":"Transcript ready"}`)
		case r.URL.Path == "/api/tasks/t1":
			fmt.Fprint(w, `{"id":"t1","title":"Video","status":"idle","activity":"Transcript ready"}`)
		case r.URL.Path == "/api/tasks/t1/transcript":
			fmt.Fprint(w, source)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tl := &tools{hub: newHubClient(srv.URL, "hub-token")}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "fbwatch-test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "video", Description: "Read a transcript page"}, tl.video)
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var rebuilt strings.Builder
	offset := 0
	for {
		args := map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ?t=2700s"}
		if offset > 0 {
			args["offset"] = offset
		}
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "video", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("MCP call failed: result=%v err=%v", res, err)
		}
		p := readTranscriptPage(t, res.Content[0].(*mcp.TextContent).Text)
		rebuilt.WriteString(p.body)
		if !p.hasMore {
			break
		}
		if p.next <= offset {
			t.Fatal("pagination did not advance")
		}
		offset = p.next
	}
	if rebuilt.String() != source || created != 1 {
		t.Fatalf("expected complete source and one Hub task; got %d tasks", created)
	}
	res, _, err := tl.video(context.Background(), nil, videoIn{URL: "https://youtu.be/dQw4w9WgXcQ", Offset: utf8.RuneCountInString(source) + 1})
	if err != nil || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "分頁讀取失敗") {
		t.Fatalf("offset past EOF: result=%v err=%v", res, err)
	}
}

func TestHubTranscriptRejectsIncompleteInput(t *testing.T) {
	for name, source := range map[string]string{
		"over byte limit": strings.Repeat("x", (4<<20)+1),
		"invalid UTF-8":   "[0:00] x\xff",
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, source)
			}))
			defer srv.Close()
			if text, err := newHubClient(srv.URL, "hub-token").transcript(context.Background(), "t1"); err == nil || text != "" {
				t.Fatalf("incomplete transcript must fail explicitly: len=%d err=%v", len(text), err)
			}
		})
	}
}
