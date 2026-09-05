package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

func runtime() config.Runtime {
	return config.Runtime{
		BaseURL:       "https://members.example.test",
		SessionSecret: []byte("a-secret-long-enough-to-be-a-secret"),
		SessionMaxAge: time.Hour,
		Accounts: map[string]config.Role{
			"ny@example.test":        config.RoleIntake,
			"ekonomi@example.test":   config.RoleCashier,
			"styrelsen@example.test": config.RoleBoard,
		},
		Google: config.GoogleSettings{
			ClientID: "client-id", ClientSecret: "shh", HostedDomain: "example.test",
		},
	}
}

// issue returns a request carrying a freshly minted session for an address.
func issue(t *testing.T, g *Guard, email string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	g.Issue(rec, email, "Anna Andersson")
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestASessionRoundTrips(t *testing.T) {
	g := New(runtime())
	session, ok := g.Session(issue(t, g, "Ekonomi@Example.test"))
	if !ok {
		t.Fatal("a freshly issued session was not accepted")
	}
	if session.Email != "ekonomi@example.test" {
		t.Errorf("email: got %q, want it lowercased", session.Email)
	}
	if session.Role != config.RoleCashier {
		t.Errorf("role: got %q, want cashier", session.Role)
	}
	if session.Name != "Anna Andersson" {
		t.Errorf("name: got %q", session.Name)
	}
}

// The role is looked up afresh on every request rather than carried in the
// cookie. Taking an address out of the configuration has to lock it out at
// once, not whenever the cookie happens to expire.
func TestARoleIsReadFromTheConfigurationNotTheCookie(t *testing.T) {
	rt := runtime()
	g := New(rt)
	req := issue(t, g, "ekonomi@example.test")

	delete(rt.Accounts, "ekonomi@example.test")
	if _, ok := New(rt).Session(req); ok {
		t.Fatal("a session survived its account being removed from the configuration")
	}
}

func TestASessionSignedWithAnotherSecretIsRefused(t *testing.T) {
	g := New(runtime())
	req := issue(t, g, "styrelsen@example.test")

	other := runtime()
	other.SessionSecret = []byte("a completely different secret")
	if _, ok := New(other).Session(req); ok {
		t.Fatal("a forged session was accepted")
	}
}

func TestATamperedSessionIsRefused(t *testing.T) {
	g := New(runtime())
	rec := httptest.NewRecorder()
	g.Issue(rec, "ny@example.test", "Ny")
	cookie := rec.Result().Cookies()[0]

	// Swap the intake account for the board's, keeping the signature.
	tampered := strings.Replace(cookie.Value, "ny@example.test", "styrelsen@example.test", 1)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookie.Name, Value: tampered})

	if _, ok := g.Session(req); ok {
		t.Fatal("an edited session was accepted; the whole permission model rests on this")
	}
}

func TestAnExpiredSessionIsRefused(t *testing.T) {
	rt := runtime()
	g := New(rt)
	// Mint it as though it were issued two hours ago, with a one-hour life.
	g.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	req := issue(t, g, "styrelsen@example.test")

	g.now = time.Now
	if _, ok := g.Session(req); ok {
		t.Fatal("an expired session was accepted")
	}
}

func TestNoCookieIsNoSession(t *testing.T) {
	g := New(runtime())
	if _, ok := g.Session(httptest.NewRequest("GET", "/", nil)); ok {
		t.Fatal("a request with no cookie was signed in")
	}
}

func TestClearRemovesTheCookie(t *testing.T) {
	g := New(runtime())
	rec := httptest.NewRecorder()
	g.Clear(rec)
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			if c.MaxAge >= 0 || c.Value != "" {
				t.Errorf("the cookie was not cleared: %+v", c)
			}
			return
		}
	}
	t.Fatal("no cookie was written at all")
}

func TestCookiesAreSecureBehindHTTPS(t *testing.T) {
	rec := httptest.NewRecorder()
	New(runtime()).Issue(rec, "ny@example.test", "Ny")
	c := rec.Result().Cookies()[0]
	if !c.Secure {
		t.Error("the session cookie is not marked Secure on an https deployment")
	}
	if !c.HttpOnly {
		t.Error("the session cookie is readable from JavaScript")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite: got %v, want Lax", c.SameSite)
	}

	plain := runtime()
	plain.BaseURL = "http://localhost:8080"
	rec = httptest.NewRecorder()
	New(plain).Issue(rec, "ny@example.test", "Ny")
	if rec.Result().Cookies()[0].Secure {
		t.Error("a Secure cookie on plain http would never come back")
	}
}

func TestStartBuildsAGoogleSignInAndPinsTheReply(t *testing.T) {
	g := New(runtime())
	rec := httptest.NewRecorder()
	where, err := g.Start(rec, "/avgifter")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"https://accounts.google.com/o/oauth2/v2/auth",
		"client_id=client-id",
		"response_type=code",
		"hd=example.test",
		"prompt=select_account",
		"redirect_uri=https%3A%2F%2Fmembers.example.test%2Foauth2%2Fcallback",
	} {
		if !strings.Contains(where, want) {
			t.Errorf("the sign-in address is missing %q:\n%s", want, where)
		}
	}
	// The state cookie is what ties the reply back to this request. Without
	// it, somebody who can set a cookie could pin a victim to their own
	// sign-in.
	var state *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookie {
			state = c
		}
	}
	if state == nil || state.Value == "" {
		t.Fatal("no state cookie was written")
	}
	if !state.HttpOnly {
		t.Error("the state cookie is readable from JavaScript")
	}
}

// The claim checks are the whole of the front door. Each of these would let
// somebody in who has no business here.
func TestClaimsAreChecked(t *testing.T) {
	g := New(runtime())
	good := Claims{
		Issuer: "https://accounts.google.com", Audience: "client-id",
		Email: "styrelsen@example.test", EmailVerified: true,
		HostedDomain: "example.test", Expires: time.Now().Add(time.Hour).Unix(),
	}
	if err := g.checkClaims(good); err != nil {
		t.Fatalf("a good token was refused: %v", err)
	}
	// The other spelling Google uses.
	plain := good
	plain.Issuer = "accounts.google.com"
	if err := g.checkClaims(plain); err != nil {
		t.Errorf("the bare issuer was refused: %v", err)
	}

	tests := []struct {
		name  string
		alter func(*Claims)
	}{
		{"another issuer", func(c *Claims) { c.Issuer = "https://evil.test" }},
		{"another application's token", func(c *Claims) { c.Audience = "someone-else" }},
		{"an unverified address", func(c *Claims) { c.EmailVerified = false }},
		{"a personal Google account", func(c *Claims) { c.HostedDomain = "" }},
		{"another Workspace", func(c *Claims) { c.HostedDomain = "elsewhere.test" }},
		{"an expired token", func(c *Claims) { c.Expires = time.Now().Add(-time.Hour).Unix() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := good
			tc.alter(&c)
			if err := g.checkClaims(c); err == nil {
				t.Error("was let in")
			}
		})
	}
}

func TestParseIDTokenReadsTheClaims(t *testing.T) {
	// header.payload.signature, where the payload is the interesting third.
	token := "eyJhbGciOiJSUzI1NiJ9." +
		"eyJpc3MiOiJodHRwczovL2FjY291bnRzLmdvb2dsZS5jb20iLCJlbWFpbCI6ImFAYi50ZXN0IiwiaGQiOiJiLnRlc3QifQ." +
		"signature"
	claims, err := parseIDToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != "a@b.test" || claims.HostedDomain != "b.test" {
		t.Errorf("got %+v", claims)
	}

	if _, err := parseIDToken("not-a-token"); err == nil {
		t.Error("nonsense was parsed as a token")
	}
}

func TestIDsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := ID()
		if seen[id] {
			t.Fatalf("ID() repeated %q after %d draws", id, i)
		}
		seen[id] = true
	}
}
