package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/mattermost"
)

// fakeChat stands in for the house's Mattermost server. It answers the three
// calls the registry makes and records what was posted, so a test can assert
// on the message a channel would really have received.
type fakeChat struct {
	*httptest.Server

	mu    sync.Mutex
	posts []sentPost
	// directs maps a made-up direct-channel id back to the user it belongs
	// to, so a test can tell a direct message from a channel announcement.
	directs map[string]string
}

// sentPost is one message the bot delivered.
type sentPost struct {
	Channel string
	Message string
	// DMTo is the user id when the post went to a direct channel.
	DMTo string
}

const fakeBotID = "bot0000000000000000000000"

func newFakeChat(t *testing.T) *fakeChat {
	t.Helper()
	f := &fakeChat{directs: map[string]string{}}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v4/users/me", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mattermost.User{
			ID: fakeBotID, Username: "members", IsBot: true,
		})
	})

	// A direct channel is created on demand, exactly as the real server does.
	mux.HandleFunc("POST /api/v4/channels/direct", func(w http.ResponseWriter, r *http.Request) {
		var ids []string
		if err := json.NewDecoder(r.Body).Decode(&ids); err != nil || len(ids) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		to := ids[1]
		id := "dm-" + to
		f.mu.Lock()
		f.directs[id] = to
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	})

	mux.HandleFunc("POST /api/v4/posts", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ChannelID string `json:"channel_id"`
			Message   string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.posts = append(f.posts, sentPost{
			Channel: p.ChannelID, Message: p.Message, DMTo: f.directs[p.ChannelID],
		})
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": "post-1"})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// client is a registry bot pointed at the fake server.
func (f *fakeChat) client(t *testing.T) *mattermost.Client {
	t.Helper()
	return mattermost.New(f.URL, "test-token", discardLogger())
}

// sent returns everything posted so far. Announcements are made in a
// goroutine, so tests call waitFor first.
func (f *fakeChat) sent() []sentPost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentPost(nil), f.posts...)
}

// only returns the single post whose message mentions the given text, and
// fails when there is not exactly one. Asserting on "the message about Anna"
// reads better than asserting on an index into a slice that a background
// goroutine fills in.
func (f *fakeChat) only(t *testing.T, contains string) sentPost {
	t.Helper()
	var hits []sentPost
	for _, p := range f.sent() {
		if strings.Contains(p.Message, contains) {
			hits = append(hits, p)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one message mentioning %q, got %d: %+v", contains, len(hits), f.sent())
	}
	return hits[0]
}

// discardLogger keeps test output about what the bot did out of the way.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitFor blocks until n messages have arrived, or fails. Announcements are
// deliberately made in a goroutine so that a slow chat server cannot slow a
// request down, which means a test has to wait for them rather than assume
// they have already happened.
func (f *fakeChat) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.posts)
		f.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("waited for %d messages, got %d: %+v", n, len(f.posts), f.posts)
}

// quiet asserts that nothing was announced. It waits a moment first, because
// proving a negative about a goroutine needs at least a chance for it to run.
func (f *fakeChat) quiet(t *testing.T) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	if got := f.sent(); len(got) != 0 {
		t.Fatalf("expected no announcements, got %+v", got)
	}
}

// mattermostDisabled is a client with no server, which logs instead of
// sending. It is what a deployment without Mattermost has.
func mattermostDisabled() *mattermost.Client {
	return mattermost.New("", "", discardLogger())
}
