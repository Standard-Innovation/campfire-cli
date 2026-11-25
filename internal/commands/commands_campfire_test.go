package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/cli/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// fakeCampfire serves the slice of the Campfire API the CLI uses. Room 4 holds
// messages 1..100, served 40 to a page like Campfire does. Room 5 is the same
// except that messages 11 and 100 have no id. Bot and room writes are recorded
// in writes; the token "member" belongs to someone who is not an administrator.
type fakeCampfire struct {
	*httptest.Server
	posts  []map[string]any
	writes []fakeWrite
}

type fakeWrite struct {
	method, path string
	body         map[string]any
}

const (
	fakeBotKey    = "12-createdKEY123abc"
	fakeNewBotKey = "12-rekeyedKEY456def"
)

func newFakeCampfire(t *testing.T) *fakeCampfire {
	t.Helper()
	f := &fakeCampfire{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /users/me/profile.json", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(w, r) {
			return
		}
		writeJSON(w, map[string]any{"id": 1, "name": "Tom", "email_address": "tom@example.com"})
	})
	mux.HandleFunc("GET /rooms.json", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(w, r) {
			return
		}
		writeJSON(w, []any{
			map[string]any{"id": 4, "name": "General", "type": "open", "involvement": "mentions"},
			map[string]any{"id": 7, "name": nil, "type": "direct", "involvement": "everything", "users": []any{
				map[string]any{"id": 1, "name": "Tom"}, map[string]any{"id": 2, "name": "Sam"},
			}},
			map[string]any{"id": 8, "name": nil, "type": "direct", "involvement": "everything", "users": []any{
				map[string]any{"id": 1, "name": "Tom"}, map[string]any{"id": 2, "name": "Sam"}, map[string]any{"id": 3, "name": "Ana"},
			}},
			map[string]any{"id": 9, "name": "Ops", "type": "open", "involvement": "mentions"},
			map[string]any{"id": 10, "name": "ops", "type": "closed", "involvement": "everything"},
		})
	})
	messages := func(w http.ResponseWriter, r *http.Request, nextPath string) {
		if r.PathValue("room") != "4" && r.PathValue("room") != "5" && r.PathValue("room") != "7" {
			http.NotFound(w, r)
			return
		}
		lo, hi := 61, 100
		if b := r.URL.Query().Get("before"); b != "" {
			n, _ := strconv.Atoi(b)
			lo, hi = max(1, n-40), n-1
		}
		if a := r.URL.Query().Get("after"); a != "" {
			n, _ := strconv.Atoi(a)
			lo, hi = n+1, min(100, n+40)
		}
		page := []any{}
		for id := lo; id <= hi; id++ {
			m := map[string]any{"id": id, "body": map[string]any{"plain_text": fmt.Sprintf("m%d", id)}, "creator": map[string]any{"name": "Sam"}}
			if r.PathValue("room") == "5" && (id == 11 || id == 100) {
				delete(m, "id")
			}
			page = append(page, m)
		}
		w.Header().Set("X-Total-Count", "100")
		switch {
		case r.URL.Query().Get("after") != "" && hi < 100:
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?after=%d>; rel="next"`, f.URL, nextPath, hi))
		case r.URL.Query().Get("after") == "" && lo > 1:
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?before=%d>; rel="next"`, f.URL, nextPath, lo))
		}
		writeJSON(w, page)
	}
	mux.HandleFunc("GET /rooms/{room}/messages.json", func(w http.ResponseWriter, r *http.Request) {
		if f.authorized(w, r) {
			messages(w, r, r.URL.Path)
		}
	})
	mux.HandleFunc("GET /rooms/{room}/{key}/messages", func(w http.ResponseWriter, r *http.Request) {
		messages(w, r, r.URL.Path)
	})
	mux.HandleFunc("POST /rooms/{room}/messages.json", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(w, r) {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.posts = append(f.posts, map[string]any{"room": r.PathValue("room"), "body": body})
		w.Header().Set("Location", f.URL+"/rooms/"+r.PathValue("room")+"/messages/101")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": 101, "url": f.URL + "/rooms/" + r.PathValue("room") + "/@101"})
	})
	mux.HandleFunc("POST /rooms/{room}/{key}/messages", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.posts = append(f.posts, map[string]any{"room": r.PathValue("room"), "key": r.PathValue("key"), "text": string(b)})
		w.Header().Set("Location", f.URL+"/rooms/"+r.PathValue("room")+"/messages/102")
		w.WriteHeader(http.StatusCreated)
	})

	mux.HandleFunc("GET /account/bots.json", func(w http.ResponseWriter, r *http.Request) {
		if f.administrator(w, r) {
			writeJSON(w, []any{map[string]any{"id": 12, "name": "Deploy Bot"}, map[string]any{"id": 13, "name": "Alerts"}})
		}
	})
	mux.HandleFunc("POST /account/bots.json", func(w http.ResponseWriter, r *http.Request) {
		if !f.administrator(w, r) {
			return
		}
		body := f.record(r)
		name, _ := toMap(body["user"])["name"].(string)
		switch name {
		case "":
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		case "Taken":
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(w, map[string]any{"status": 422, "error": "Name has already been taken"})
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": 12, "name": name, "bot_key": fakeBotKey})
	})
	mux.HandleFunc("PUT /account/bots/{id}/key.json", func(w http.ResponseWriter, r *http.Request) {
		if !f.administrator(w, r) {
			return
		}
		f.record(r)
		if r.PathValue("id") != "12" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"id": 12, "name": "Deploy Bot", "bot_key": fakeNewBotKey})
	})
	mux.HandleFunc("DELETE /account/bots/{id}", func(w http.ResponseWriter, r *http.Request) {
		if f.administrator(w, r) {
			f.record(r)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	createRoom := func(roomType string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !f.administrator(w, r) {
				return
			}
			body := f.record(r)
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"id": 21, "name": toMap(body["room"])["name"], "type": roomType})
		}
	}
	mux.HandleFunc("POST /rooms/closeds.json", createRoom("closed"))
	mux.HandleFunc("POST /rooms/opens.json", createRoom("open"))
	mux.HandleFunc("PUT /rooms/closeds/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !f.administrator(w, r) {
			return
		}
		body := f.record(r)
		if r.PathValue("id") == "4.json" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, map[string]any{"id": 10, "name": toMap(body["room"])["name"], "type": "closed"})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCampfire) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Application"`)
		http.Error(w, "HTTP Token: Access denied.", http.StatusUnauthorized)
		return false
	}
	return true
}

// administrator lets "tok" through, refuses "member" with a bare 403, and
// answers anything else 401.
func (f *fakeCampfire) administrator(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") == "Bearer member" {
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	return f.authorized(w, r)
}

func (f *fakeCampfire) record(r *http.Request) map[string]any {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.writes = append(f.writes, fakeWrite{method: r.Method, path: r.URL.Path, body: body})
	return body
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// run executes the CLI against the fake server and returns the JSON envelope.
func run(t *testing.T, f *fakeCampfire, token string, args ...string) (map[string]any, error) {
	t.Helper()
	if _, err := runRaw(t, f, token, args...); err != nil {
		return nil, err
	}
	var env map[string]any
	if jsonErr := json.Unmarshal([]byte(TestOutput()), &env); jsonErr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jsonErr, TestOutput())
	}
	return env, nil
}

// runRaw executes the CLI against the fake server and returns its raw output.
func runRaw(t *testing.T, f *fakeCampfire, token string, args ...string) (string, error) {
	t.Helper()
	ResetTestMode()
	resetFlags(rootCmd)
	SetTestMode(f.URL, token)
	t.Cleanup(ResetTestMode)

	rootCmd.SetArgs(args)
	_, err := rootCmd.ExecuteC()
	return TestOutput(), err
}

// resetFlags restores every flag to its default so runs don't leak state.
func resetFlags(cmd *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if slice, ok := f.Value.(pflag.SliceValue); ok {
			_ = slice.Replace(nil)
			f.Changed = false
			return
		}
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	}
	cmd.Flags().VisitAll(reset)
	cmd.PersistentFlags().VisitAll(reset)
	for _, c := range cmd.Commands() {
		resetFlags(c)
	}
}

func ids(t *testing.T, data any) []int {
	t.Helper()
	items, _ := data.([]any)
	out := make([]int, len(items))
	for i, item := range items {
		out[i] = int(item.(map[string]any)["id"].(float64))
	}
	return out
}

func TestMessageListLatestPage(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "message", "list", "--room", "4")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(t, env["data"])
	if len(got) != 40 || got[0] != 61 || got[39] != 100 {
		t.Fatalf("ids = %v", got)
	}
	ctx := env["context"].(map[string]any)
	if ctx["total_count"].(float64) != 100 {
		t.Errorf("total_count = %v", ctx["total_count"])
	}
	if p := ctx["pagination"].(map[string]any); p["next"] != "--before 61" {
		t.Errorf("pagination = %v", p)
	}
}

func TestMessageListLimitFetchesOlderPagesAndKeepsNewest(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "message", "list", "--room", "4", "--limit", "50")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(t, env["data"])
	if len(got) != 50 || got[0] != 51 || got[49] != 100 {
		t.Fatalf("ids = %v..%v (%d)", got[0], got[len(got)-1], len(got))
	}
	// Two pages fetched 21..100; paging on must start below the kept 51, not 21.
	if p := env["context"].(map[string]any)["pagination"].(map[string]any); p["next"] != "--before 51" {
		t.Errorf("pagination = %v", p)
	}
	if env["summary"] != "50 messages" {
		t.Errorf("summary = %v", env["summary"])
	}
	if _, ok := env["notice"]; ok {
		t.Errorf("notice = %v", env["notice"])
	}
}

func TestMessageListLimitForwardPagesOnFromTheLastKept(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "message", "list", "--room", "4", "--after", "10", "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(t, env["data"]); len(got) != 1 || got[0] != 11 {
		t.Fatalf("ids = %v", got)
	}
	if p := env["context"].(map[string]any)["pagination"].(map[string]any); p["next"] != "--after 11" {
		t.Errorf("pagination = %v", p)
	}
	if env["summary"] != "1 message" {
		t.Errorf("summary = %v", env["summary"])
	}
}

// The last page has no next link, but the messages cut from it still exist.
func TestMessageListLimitOnTheLastPageStillPagesOn(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "message", "list", "--room", "4", "--after", "90", "--limit", "5")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(t, env["data"]); len(got) != 5 || got[0] != 91 || got[4] != 95 {
		t.Fatalf("ids = %v", got)
	}
	if p := env["context"].(map[string]any)["pagination"].(map[string]any); p["next"] != "--after 95" {
		t.Errorf("pagination = %v", p)
	}
}

func TestMessageListLimitFallsBackToTheServerCursorWithoutAnID(t *testing.T) {
	f := newFakeCampfire(t)
	for args, want := range map[string]string{"--limit 1": "--before 61", "--after 10 --limit 1": "--after 50"} {
		env, err := run(t, f, "tok", append([]string{"message", "list", "--room", "5"}, strings.Fields(args)...)...)
		if err != nil {
			t.Fatal(err)
		}
		if p := env["context"].(map[string]any)["pagination"].(map[string]any); p["next"] != want {
			t.Errorf("%s: pagination = %v, want %s", args, p, want)
		}
	}
}

func TestMessageListAllIsChronological(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "message", "list", "--room", "4", "--all")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(t, env["data"])
	if len(got) != 100 {
		t.Fatalf("got %d messages", len(got))
	}
	for i, id := range got {
		if id != i+1 {
			t.Fatalf("out of order at %d: %v", i, id)
		}
	}
}

func TestMessageListAfterPagesForward(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "message", "list", "--room", "4", "--after", "10")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(t, env["data"])
	if got[0] != 11 || got[len(got)-1] != 50 {
		t.Fatalf("ids = %v", got)
	}
	if p := env["context"].(map[string]any)["pagination"].(map[string]any); p["next"] != "--after 50" {
		t.Errorf("pagination = %v", p)
	}
}

func TestMessageListWithBotKeyNeverEchoesTheKey(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "12-secretBotKey99", "message", "list", "--room", "4")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(TestOutput(), "secretBotKey") {
		t.Fatalf("bot key leaked into output:\n%s", TestOutput())
	}
}

func TestMessagePostAsPersonToDirectMessageByName(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "message", "post", "--room", "sam", "Got a minute?")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 1 || f.posts[0]["room"] != "7" {
		t.Fatalf("posts = %v", f.posts)
	}
	body := f.posts[0]["body"].(map[string]any)["message"].(map[string]any)["body"]
	if body != "Got a minute?" {
		t.Errorf("body = %v", body)
	}
	if loc := env["context"].(map[string]any)["location"]; !strings.HasSuffix(loc.(string), "/rooms/7/@101") {
		t.Errorf("location = %v", loc)
	}
}

func TestMessagePostAsBot(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "12-abcDEF123", "message", "post", "--room", "https://x/rooms/4/@9", "-b", "Deployed")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 1 || f.posts[0]["room"] != "4" || f.posts[0]["text"] != "Deployed" {
		t.Fatalf("posts = %v", f.posts)
	}
	if data := env["data"].(map[string]any); !strings.HasSuffix(data["url"].(string), "/rooms/4/messages/102") {
		t.Errorf("data = %v", data)
	}
}

func TestMessagePostRejectsFlagAndArgument(t *testing.T) {
	f := newFakeCampfire(t)
	if _, err := run(t, f, "tok", "message", "post", "--room", "4", "-b", "a", "b"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRoomNameAmbiguityAndNotFound(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "tok", "message", "list", "--room", "ops")
	if e := output.AsError(err); e.Code != output.CodeAmbiguous {
		t.Errorf("expected ambiguity for ops, got %v", err)
	}
	// Your own name is not a room: it must not pick one of your DMs.
	_, err = run(t, f, "tok", "message", "list", "--room", "Tom")
	if e := output.AsError(err); e.Code != output.CodeNotFound {
		t.Errorf("expected not found for Tom, got %v", err)
	}
}

func TestRoomListAddsDisplayNames(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "room", "list")
	if err != nil {
		t.Fatal(err)
	}
	rooms := env["data"].([]any)
	if name := rooms[1].(map[string]any)["display_name"]; name != "Sam" {
		t.Errorf("display_name = %v", name)
	}
	if name := rooms[2].(map[string]any)["display_name"]; name != "Ana, Sam" {
		t.Errorf("display_name = %v", name)
	}
}

func TestRoomListLimitNoticesTruncation(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "room", "list", "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(env["data"].([]any)); got != 1 {
		t.Fatalf("rooms = %d", got)
	}
	if want := "Showing 1 of 5 results"; env["notice"] != want {
		t.Errorf("notice = %v, want %q", env["notice"], want)
	}
}

func TestBotKeyRoomListExplainsTokenNeeded(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "12-abcDEF123", "room", "list")
	if err == nil || !strings.Contains(err.Error(), "personal access token") {
		t.Fatalf("got %v", err)
	}
}

func TestBadTokenIsAnAuthError(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "wrong", "auth", "status")
	if e := output.AsError(err); err == nil || e.Code != output.CodeAuth || e.ExitCode() != output.ExitAuth {
		t.Fatalf("got %v", err)
	}
}

func TestAuthStatusShowsUser(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "auth", "status")
	if err != nil {
		t.Fatal(err)
	}
	data := env["data"].(map[string]any)
	if data["authenticated"] != true || data["user"].(map[string]any)["name"] != "Tom" {
		t.Errorf("data = %v", data)
	}
}

func TestJQFilter(t *testing.T) {
	f := newFakeCampfire(t)
	ResetTestMode()
	resetFlags(rootCmd)
	SetTestMode(f.URL, "tok")
	defer ResetTestMode()
	rootCmd.SetArgs([]string{"message", "list", "--room", "4", "--limit", "2", "--jq", ".data[].body.plain_text"})
	if _, err := rootCmd.ExecuteC(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(TestOutput()); got != "m99\nm100" {
		t.Errorf("jq output = %q", got)
	}
}

func TestPageCursor(t *testing.T) {
	cases := map[string]string{
		"": "",
		"https://x/rooms/4/messages.json?before=12": "--before 12",
		"https://x/rooms/4/1-abc/messages?after=7":  "--after 7",
		"https://x/rooms/4/messages.json":           "",
	}
	for in, want := range cases {
		if got := pageCursor(in); got != want {
			t.Errorf("pageCursor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"chat.example.com":               "https://chat.example.com",
		"https://chat.example.com/":      "https://chat.example.com",
		"https://chat.example.com/rooms": "https://chat.example.com",
		"http://localhost:3000":          "http://localhost:3000",
	}
	for in, want := range cases {
		got, err := normalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeBaseURL("http://chat.example.com"); err == nil {
		t.Error("expected plain http to be refused for a remote host")
	}
}

func TestIDStringKeepsLargeIDsWhole(t *testing.T) {
	for in, want := range map[any]string{float64(4): "4", float64(1234567): "1234567", "7": "7"} {
		if got := idString(in); got != want {
			t.Errorf("idString(%v) = %q, want %q", in, got, want)
		}
	}
}

// captureStderr returns what fn writes to stderr, where --verbose logs requests.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func lastWrite(t *testing.T, f *fakeCampfire) fakeWrite {
	t.Helper()
	if len(f.writes) == 0 {
		t.Fatal("no write reached Campfire")
	}
	return f.writes[len(f.writes)-1]
}

func TestBotList(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "bot", "list")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(t, env["data"]); len(got) != 2 || got[0] != 12 {
		t.Errorf("ids = %v", got)
	}
	if env["summary"] != "2 bots" {
		t.Errorf("summary = %v", env["summary"])
	}
}

func TestBotCreateReturnsTheKeyInTheEnvelope(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "bot", "create", "--name", "Deploy Bot", "--json")
	if err != nil {
		t.Fatal(err)
	}
	w := lastWrite(t, f)
	if w.method != "POST" || w.path != "/account/bots.json" || toMap(w.body["user"])["name"] != "Deploy Bot" {
		t.Fatalf("write = %+v", w)
	}
	if _, ok := toMap(w.body["user"])["webhook_url"]; ok {
		t.Error("create must not send a webhook_url")
	}
	data := env["data"].(map[string]any)
	if data["bot_key"] != fakeBotKey || data["name"] != "Deploy Bot" {
		t.Errorf("data = %v", data)
	}
}

func TestBotCreateQuietPrintsOnlyTheKey(t *testing.T) {
	f := newFakeCampfire(t)
	out, err := runRaw(t, f, "tok", "bot", "create", "--name", "Deploy Bot", "--quiet")
	if err != nil {
		t.Fatal(err)
	}
	if out != fakeBotKey+"\n" {
		t.Errorf("output = %q", out)
	}
}

func TestBotCreateHumanOutputWarnsTheKeyIsShownOnce(t *testing.T) {
	f := newFakeCampfire(t)
	out, err := runRaw(t, f, "tok", "bot", "create", "--name", "Deploy Bot", "--styled")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(out, "12") || !strings.Contains(out, "Deploy Bot") || !strings.Contains(out, "shown once") {
		t.Errorf("output = %q", out)
	}
	if lines[len(lines)-1] != fakeBotKey {
		t.Errorf("key is not on its own last line: %q", out)
	}
}

func TestBotCreateNeedsAName(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "tok", "bot", "create", "--name", " ")
	if e := output.AsError(err); err == nil || e.Code != output.CodeUsage {
		t.Fatalf("got %v", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v", f.writes)
	}
}

func TestBotRekeyQuietPrintsOnlyTheNewKey(t *testing.T) {
	f := newFakeCampfire(t)
	out, err := runRaw(t, f, "tok", "bot", "rekey", "12", "--quiet")
	if err != nil {
		t.Fatal(err)
	}
	if w := lastWrite(t, f); w.method != "PUT" || w.path != "/account/bots/12/key.json" {
		t.Errorf("write = %+v", w)
	}
	if out != fakeNewBotKey+"\n" {
		t.Errorf("output = %q", out)
	}
}

func TestBotRekeyIDsOnlyPrintsTheBotID(t *testing.T) {
	f := newFakeCampfire(t)
	out, err := runRaw(t, f, "tok", "bot", "rekey", "12", "--ids-only")
	if err != nil {
		t.Fatal(err)
	}
	if out != "12\n" {
		t.Errorf("output = %q", out)
	}
}

func TestBotRekeyRefusesANonNumericID(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "tok", "bot", "rekey", "../rooms")
	if e := output.AsError(err); err == nil || e.Code != output.CodeUsage {
		t.Fatalf("got %v", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v", f.writes)
	}
}

func TestBotVerboseNeverLogsTheKey(t *testing.T) {
	f := newFakeCampfire(t)
	for _, args := range [][]string{
		{"bot", "create", "--name", "Deploy Bot", "--verbose", "--json"},
		{"bot", "rekey", "12", "--verbose", "--json"},
	} {
		var err error
		stderr := captureStderr(t, func() { _, err = runRaw(t, f, "tok", args...) })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr, "> ") {
			t.Fatalf("%v: no verbose request log: %q", args, stderr)
		}
		if strings.Contains(stderr, "KEY") {
			t.Errorf("%v: key in verbose output: %q", args, stderr)
		}
	}
}

func TestBotDeleteAsksFirst(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "tok", "bot", "delete", "12")
	if e := output.AsError(err); err == nil || e.Code != output.CodeUsage || !strings.Contains(e.Message, "--yes") {
		t.Fatalf("got %v", err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("deleted without confirmation: %v", f.writes)
	}

	for _, flag := range []string{"--yes", "--agent"} {
		f.writes = nil
		if _, err := runRaw(t, f, "tok", "bot", "delete", "12", flag); err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		if w := lastWrite(t, f); w.method != "DELETE" || w.path != "/account/bots/12.json" {
			t.Errorf("%s: write = %+v", flag, w)
		}
	}
}

func TestBotDeleteReportsTheDeletion(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "bot", "delete", "12", "-y")
	if err != nil {
		t.Fatal(err)
	}
	if data := env["data"].(map[string]any); data["id"] != "12" || data["deleted"] != true {
		t.Errorf("data = %v", data)
	}
}

func TestBotForbiddenExplainsTheTokenNeeded(t *testing.T) {
	f := newFakeCampfire(t)
	for _, args := range [][]string{
		{"bot", "list"},
		{"bot", "create", "--name", "x"},
		{"bot", "rekey", "12"},
		{"bot", "delete", "12", "--yes"},
	} {
		_, err := run(t, f, "member", args...)
		e := output.AsError(err)
		if err == nil || e.Code != output.CodeForbidden || e.ExitCode() != output.ExitForbidden {
			t.Fatalf("%v: got %v", args, err)
		}
		if !strings.Contains(e.Message, "write token") || !strings.Contains(e.Message, "administrator") {
			t.Errorf("%v: message = %q", args, e.Message)
		}
	}
}

func TestBotCreateInvalidExplainsOrKeepsTheServerReason(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "tok", "bot", "create", "--name", "Taken")
	if e := output.AsError(err); e.Message != "Name has already been taken" || !strings.Contains(e.Hint, "rejected") {
		t.Errorf("server reason: message = %q, hint = %q", e.Message, e.Hint)
	}
}

func TestBotCommandsNeedAPersonalToken(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "12-abcDEF123", "bot", "list")
	if err == nil || !strings.Contains(err.Error(), "personal access token") {
		t.Fatalf("got %v", err)
	}
}

func TestRoomCreateClosedWithMembers(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "room", "create", "--name", "Deploys", "--member", "12", "--member", "2,3")
	if err != nil {
		t.Fatal(err)
	}
	w := lastWrite(t, f)
	if w.method != "POST" || w.path != "/rooms/closeds.json" || toMap(w.body["room"])["name"] != "Deploys" {
		t.Fatalf("write = %+v", w)
	}
	if got := fmt.Sprint(w.body["user_ids"]); got != "[12 2 3]" {
		t.Errorf("user_ids = %s", got)
	}
	if data := env["data"].(map[string]any); data["id"] != float64(21) || data["type"] != "closed" {
		t.Errorf("data = %v", data)
	}
}

func TestRoomCreateClosedWithoutMembersSendsAnEmptyList(t *testing.T) {
	f := newFakeCampfire(t)
	if _, err := run(t, f, "tok", "room", "create", "--name", "Solo"); err != nil {
		t.Fatal(err)
	}
	if ids, ok := lastWrite(t, f).body["user_ids"].([]any); !ok || len(ids) != 0 {
		t.Errorf("user_ids = %v", lastWrite(t, f).body["user_ids"])
	}
}

func TestRoomCreateOpen(t *testing.T) {
	f := newFakeCampfire(t)
	if _, err := run(t, f, "tok", "room", "create", "--name", "Announcements", "--open"); err != nil {
		t.Fatal(err)
	}
	w := lastWrite(t, f)
	if w.path != "/rooms/opens.json" || toMap(w.body["room"])["name"] != "Announcements" {
		t.Fatalf("write = %+v", w)
	}
	if _, ok := w.body["user_ids"]; ok {
		t.Errorf("open room sent user_ids: %v", w.body)
	}
}

func TestRoomCreateRefusesOpenWithMembersAndBadIDs(t *testing.T) {
	f := newFakeCampfire(t)
	for _, args := range [][]string{
		{"room", "create", "--name", "x", "--open", "--member", "2"},
		{"room", "create", "--name", "x", "--member", "sam"},
		{"room", "create", "--member", "2"},
	} {
		_, err := run(t, f, "tok", args...)
		if e := output.AsError(err); err == nil || e.Code != output.CodeUsage {
			t.Errorf("%v: got %v", args, err)
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v", f.writes)
	}
}

func TestRoomCreateAndUpdatePrintTheIDForQuietAndIDsOnly(t *testing.T) {
	f := newFakeCampfire(t)
	for args, want := range map[string]string{
		"room create --name Deploys --quiet":              "21\n",
		"room create --name Deploys --ids-only":           "21\n",
		"room update 10 --name Ops --member 2 --quiet":    "10\n",
		"room update 10 --name Ops --member 2 --ids-only": "10\n",
	} {
		out, err := runRaw(t, f, "tok", strings.Fields(args)...)
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		if out != want {
			t.Errorf("%s: output = %q, want %q", args, out, want)
		}
	}
}

func TestRoomUpdateReplacesMembersAndKeepsTheName(t *testing.T) {
	f := newFakeCampfire(t)
	env, err := run(t, f, "tok", "room", "update", "10", "--member", "2", "--member", "12")
	if err != nil {
		t.Fatal(err)
	}
	w := lastWrite(t, f)
	if w.method != "PUT" || w.path != "/rooms/closeds/10.json" {
		t.Fatalf("write = %+v", w)
	}
	if name := toMap(w.body["room"])["name"]; name != "ops" {
		t.Errorf("name = %v, want the room's current name", name)
	}
	if got := fmt.Sprint(w.body["user_ids"]); got != "[2 12]" {
		t.Errorf("user_ids = %s", got)
	}
	if env["summary"] != "Updated room 10" {
		t.Errorf("summary = %v", env["summary"])
	}
}

func TestRoomUpdateRenames(t *testing.T) {
	f := newFakeCampfire(t)
	if _, err := run(t, f, "tok", "room", "update", "10", "--name", "Releases", "--member", "2"); err != nil {
		t.Fatal(err)
	}
	if name := toMap(lastWrite(t, f).body["room"])["name"]; name != "Releases" {
		t.Errorf("name = %v", name)
	}
}

func TestRoomUpdateNeedsMembers(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "tok", "room", "update", "10", "--name", "Releases")
	if e := output.AsError(err); err == nil || e.Code != output.CodeUsage {
		t.Fatalf("got %v", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v", f.writes)
	}
}

func TestRoomUpdateOfAnOpenRoomExplainsTheRefusal(t *testing.T) {
	f := newFakeCampfire(t)
	_, err := run(t, f, "tok", "room", "update", "4", "--name", "General", "--member", "2")
	e := output.AsError(err)
	if err == nil || e.HTTPStatus != 422 || !strings.Contains(e.Message, "Open rooms") {
		t.Fatalf("got %v (%q)", err, e.Message)
	}
}

func TestRoomWritesForbiddenExplainTheTokenNeeded(t *testing.T) {
	f := newFakeCampfire(t)
	for _, args := range [][]string{
		{"room", "create", "--name", "x"},
		{"room", "update", "10", "--name", "x", "--member", "2"},
	} {
		_, err := run(t, f, "member", args...)
		e := output.AsError(err)
		if err == nil || e.Code != output.CodeForbidden || !strings.Contains(e.Message, "write token") {
			t.Errorf("%v: got %v", args, err)
		}
	}
}
