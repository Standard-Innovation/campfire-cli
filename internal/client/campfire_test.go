package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsBotKey(t *testing.T) {
	cases := map[string]bool{
		"12-abcDEF123":             true,
		"1-x":                      true,
		"xxxxxxxxxxxxxxxxxxxxxxxx": false,
		"12-abc-def":               false,
		"abc-123":                  false,
		"":                         false,
	}
	for in, want := range cases {
		if got := IsBotKey(in); got != want {
			t.Errorf("IsBotKey(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRedactBotKeys(t *testing.T) {
	in := "https://chat.example.com/rooms/4/12-abcDEF123456/messages?before=9"
	want := "https://chat.example.com/rooms/4/[BOT_KEY]/messages?before=9"
	if got := RedactBotKeys(in); got != want {
		t.Errorf("RedactBotKeys = %q, want %q", got, want)
	}
	if got := RedactBotKeys("/rooms/4/messages.json"); got != "/rooms/4/messages.json" {
		t.Errorf("RedactBotKeys changed a token path: %q", got)
	}
}

func TestBuildURLRefusesOtherOrigins(t *testing.T) {
	c := New("https://chat.example.com", "secret")
	if _, err := c.buildURL("https://evil.example.net/rooms/1/messages.json"); err == nil {
		t.Fatal("expected an error for a foreign origin")
	}
	if _, err := c.buildURL("http://chat.example.com/rooms"); err == nil {
		t.Fatal("expected an error for a scheme downgrade")
	}
	got, err := c.buildURL("https://chat.example.com/rooms/1/messages.json?before=2")
	if err != nil || got != "https://chat.example.com/rooms/1/messages.json?before=2" {
		t.Fatalf("same-origin URL: got %q, %v", got, err)
	}
}

func TestPostWithTokenSendsJSON(t *testing.T) {
	var gotPath, gotAuth, gotType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotType = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Location", "/rooms/4/messages/9")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":9}`))
	}))
	defer srv.Close()

	cf, _ := NewCampfire(srv.URL, "tok123")
	resp, err := cf.Post("4", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/rooms/4/messages.json" || gotAuth != "Bearer tok123" || gotType != "application/json" {
		t.Errorf("path=%q auth=%q type=%q", gotPath, gotAuth, gotType)
	}
	if msg, _ := gotBody["message"].(map[string]any); msg["body"] != "hello" {
		t.Errorf("body = %v", gotBody)
	}
	if resp.Location != "/rooms/4/messages/9" {
		t.Errorf("location = %q", resp.Location)
	}
}

func TestPostWithBotKeySendsText(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	cf, _ := NewCampfire(srv.URL, "12-abcDEF123")
	if _, err := cf.Post("4", "hello"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/rooms/4/12-abcDEF123/messages" || gotAuth != "" || gotBody != "hello" {
		t.Errorf("path=%q auth=%q body=%q", gotPath, gotAuth, gotBody)
	}
}

func TestBotKeysCannotListRooms(t *testing.T) {
	cf, _ := NewCampfire("https://chat.example.com", "12-abcDEF123")
	_, err := cf.Rooms()
	if err == nil || !strings.Contains(err.Error(), "personal access token") {
		t.Fatalf("expected needs-token error, got %v", err)
	}
}

func TestMessagesReadsPagingHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "before=50" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		w.Header().Set("X-Total-Count", "120")
		w.Header().Set("Link", `<`+"http://"+r.Host+`/rooms/4/messages.json?before=10>; rel="next"`)
		_, _ = w.Write([]byte(`[{"id":10}]`))
	}))
	defer srv.Close()

	cf, _ := NewCampfire(srv.URL, "tok")
	resp, err := cf.Messages("4", MessagesPage{Before: "50"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TotalCount != 120 || !strings.HasSuffix(resp.LinkNext, "before=10") {
		t.Errorf("total=%d next=%q", resp.TotalCount, resp.LinkNext)
	}
}
