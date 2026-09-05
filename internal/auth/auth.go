// Package auth is the front door: signing in with a Google Workspace account
// and remembering it in a signed cookie.
//
// There are no passwords and no accounts of its own. The association already
// has three shared mailboxes with three different jobs, and the register
// borrows them rather than inventing a fourth thing to administer.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

const (
	sessionCookie = "rb_session"
	stateCookie   = "rb_state"
)

// authEndpoint and tokenEndpoint are Google's, spelled out rather than
// discovered. Discovery would be one more thing that can be down at start-up,
// and these two addresses have not moved in a decade.
const (
	authEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint = "https://oauth2.googleapis.com/token"
)

// Session is who is signed in.
type Session struct {
	Email string
	Role  config.Role
	// Name is what Google says the account is called, for the top bar.
	Name string
}

// Guard issues and checks sessions, and drives the sign-in flow.
type Guard struct {
	rt     config.Runtime
	secret []byte
	maxAge time.Duration
	secure bool
	http   *http.Client
	now    func() time.Time
}

// New builds a Guard.
func New(rt config.Runtime) *Guard {
	return &Guard{
		rt:     rt,
		secret: rt.SessionSecret,
		maxAge: rt.SessionMaxAge,
		secure: rt.Secure(),
		http:   &http.Client{Timeout: 20 * time.Second},
		now:    time.Now,
	}
}

// Session reads and verifies the session cookie on a request.
//
// The role is looked up afresh from the configuration on every request rather
// than trusted from the cookie. Taking an address out of ACCOUNT_CASHIER and
// restarting has to lock that account out immediately; a cookie minted an
// hour ago must not still carry the old authority.
func (g *Guard) Session(r *http.Request) (Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	payload, ok := g.verify(c.Value)
	if !ok {
		return Session{}, false
	}
	email, name, ok := strings.Cut(payload, "|")
	if !ok {
		return Session{}, false
	}
	role := g.rt.Role(email)
	if !role.LoggedIn() {
		return Session{}, false
	}
	return Session{Email: email, Role: role, Name: decode(name)}, true
}

// Issue writes the session cookie for an account.
func (g *Guard) Issue(w http.ResponseWriter, email, name string) {
	exp := g.now().Add(g.maxAge)
	value := g.sign(strings.ToLower(email)+"|"+encode(name), exp)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(g.maxAge.Seconds()),
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Clear removes the session cookie.
func (g *Guard) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Start begins a sign-in. It returns the address to send the browser to, and
// writes the short-lived cookie that ties the reply back to this request.
//
// The cookie carries a random state and nonce and the page to return to. All
// three are signed together: without that, an attacker who can set a cookie
// could pin a victim's browser to their own sign-in, and the redirect target
// would be an open door to anywhere.
func (g *Guard) Start(w http.ResponseWriter, next string) (string, error) {
	state, err := randomToken()
	if err != nil {
		return "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", err
	}
	exp := g.now().Add(15 * time.Minute)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    g.sign(state+"|"+nonce+"|"+encode(next), exp),
		Path:     "/",
		Expires:  exp,
		MaxAge:   900,
		HttpOnly: true,
		Secure:   g.secure,
		// Lax rather than Strict: the browser arrives back here from
		// accounts.google.com, and Strict would withhold the cookie on
		// exactly that navigation and break every sign-in.
		SameSite: http.SameSiteLaxMode,
	})

	q := url.Values{
		"client_id":     {g.rt.Google.ClientID},
		"redirect_uri":  {g.rt.RedirectURI()},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
		"nonce":         {nonce},
		// hd narrows the account chooser to the Workspace. It is a hint, not
		// a guarantee — the claim is checked again on the way back.
		"hd": {g.rt.Google.HostedDomain},
		// The three mailboxes are shared, so somebody signing in is often
		// already signed in as themselves. Always ask which account.
		"prompt": {"select_account"},
	}
	return authEndpoint + "?" + q.Encode(), nil
}

// ErrNotAllowed is returned when a Google account signed in successfully and
// still has no business here.
var ErrNotAllowed = errors.New("that account is not one of the register's")

// Claims is the part of Google's id_token the register reads.
type Claims struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	HostedDomain  string `json:"hd"`
	Name          string `json:"name"`
	Nonce         string `json:"nonce"`
	Expires       int64  `json:"exp"`
}

// Finish completes a sign-in: it checks the state, swaps the code for an
// id_token, and satisfies itself that the account is one of ours.
//
// It returns the account and the page to go back to.
func (g *Guard) Finish(ctx context.Context, w http.ResponseWriter, r *http.Request) (Session, string, error) {
	c, err := r.Cookie(stateCookie)
	if err != nil {
		return Session{}, "", errors.New("the sign-in took too long, or cookies are blocked; try again")
	}
	// Whatever happens next, this cookie has done its job.
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: g.secure, SameSite: http.SameSiteLaxMode})

	payload, ok := g.verify(c.Value)
	if !ok {
		return Session{}, "", errors.New("the sign-in could not be verified; try again")
	}
	parts := strings.SplitN(payload, "|", 3)
	if len(parts) != 3 {
		return Session{}, "", errors.New("the sign-in could not be verified; try again")
	}
	wantState, wantNonce, next := parts[0], parts[1], decode(parts[2])

	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(wantState)) != 1 {
		return Session{}, "", errors.New("the sign-in did not come back from where it started")
	}
	if e := r.URL.Query().Get("error"); e != "" {
		return Session{}, next, fmt.Errorf("Google refused the sign-in: %s", e)
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return Session{}, next, errors.New("Google sent the browser back without a sign-in")
	}

	claims, err := g.exchange(ctx, code)
	if err != nil {
		return Session{}, next, err
	}
	if claims.Nonce != wantNonce {
		return Session{}, next, errors.New("the sign-in did not match the request that started it")
	}
	if err := g.checkClaims(claims); err != nil {
		return Session{}, next, err
	}

	email := strings.ToLower(strings.TrimSpace(claims.Email))
	role := g.rt.Role(email)
	if !role.LoggedIn() {
		return Session{}, next, fmt.Errorf("%w: %s", ErrNotAllowed, email)
	}
	return Session{Email: email, Role: role, Name: claims.Name}, next, nil
}

// exchange swaps the authorisation code for an id_token.
func (g *Guard) exchange(ctx context.Context, code string) (Claims, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {g.rt.Google.ClientID},
		"client_secret": {g.rt.Google.ClientSecret},
		"redirect_uri":  {g.rt.RedirectURI()},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.http.Do(req)
	if err != nil {
		return Claims{}, fmt.Errorf("could not reach Google to finish the sign-in: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Claims{}, fmt.Errorf("could not read Google's reply: %w", err)
	}
	if body.IDToken == "" {
		if body.Description != "" {
			return Claims{}, fmt.Errorf("Google refused the sign-in: %s", body.Description)
		}
		return Claims{}, fmt.Errorf("Google refused the sign-in: %s", body.Error)
	}
	return parseIDToken(body.IDToken)
}

// parseIDToken reads the claims out of an id_token without checking its
// signature.
//
// That is correct here and only here: the token was just fetched over TLS
// directly from Google's own token endpoint, in a request authenticated with
// the client secret, so there is no third party between us and the issuer to
// forge it. This is the code flow's server-side shortcut, and OpenID Connect
// says so in as many words. A token that arrived any other way — out of a URL
// fragment, out of a header — would have to be verified against Google's
// signing keys, and this function must never be used for one.
func parseIDToken(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("Google's reply was not a token we understand")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("could not read Google's token: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return Claims{}, fmt.Errorf("could not read Google's token: %w", err)
	}
	return claims, nil
}

// checkClaims is the rest of what the token has to say for itself.
func (g *Guard) checkClaims(c Claims) error {
	if c.Issuer != "accounts.google.com" && c.Issuer != "https://accounts.google.com" {
		return fmt.Errorf("the sign-in came from %q rather than Google", c.Issuer)
	}
	if c.Audience != g.rt.Google.ClientID {
		return errors.New("the sign-in was issued for a different application")
	}
	if c.Expires > 0 && g.now().After(time.Unix(c.Expires, 0)) {
		return errors.New("the sign-in expired on the way back; try again")
	}
	if !c.EmailVerified {
		return errors.New("that Google account has no verified address")
	}
	// The domain check is the one that matters: without it any Google account
	// in the world could reach the front door, and only the address list would
	// be standing between it and the register.
	if want := g.rt.Google.HostedDomain; want != "" &&
		!strings.EqualFold(strings.TrimSpace(c.HostedDomain), want) {
		return fmt.Errorf("only accounts in %s may sign in", want)
	}
	return nil
}

func (g *Guard) sign(payload string, exp time.Time) string {
	body := payload + "." + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, g.secret)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verify checks a signed value and returns its payload. The payload may itself
// contain dots, so the signature and expiry are taken off the end rather than
// the value being split into three.
func (g *Guard) verify(value string) (string, bool) {
	lastDot := strings.LastIndexByte(value, '.')
	if lastDot < 0 {
		return "", false
	}
	body, sig := value[:lastDot], value[lastDot+1:]
	mac := hmac.New(sha256.New, g.secret)
	mac.Write([]byte(body))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return "", false
	}
	expDot := strings.LastIndexByte(body, '.')
	if expDot < 0 {
		return "", false
	}
	exp, err := strconv.ParseInt(body[expDot+1:], 10, 64)
	if err != nil || g.now().After(time.Unix(exp, 0)) {
		return "", false
	}
	return body[:expDot], true
}

func encode(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func decode(s string) string {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand unavailable: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ID returns a random identifier, used for members and proposals. It is
// URL-safe and short enough to read out over the phone if it ever has to be.
func ID() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
