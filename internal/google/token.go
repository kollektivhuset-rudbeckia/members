// Package google talks to Google Workspace: the groups the register keeps in
// step, the address books it mirrors itself into, and the spreadsheet the
// cashiers calculate in.
//
// It is written against the REST endpoints with nothing but the standard
// library. The official client libraries would pull in a hundred modules to
// do what amounts to a signed JWT and a dozen JSON calls, and the thing that
// matters most here is not convenience but the wording of an error: when a
// synchronisation fails, the board has to be able to read why on a web page
// and act on it, which means every failure has to carry Google's own
// complaint rather than a wrapped "request failed".
package google

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// Scopes the registry asks for. They are the narrowest that will do the job,
// and they are listed here rather than spread through the call sites because
// this exact list has to be pasted into the Workspace admin console when
// domain-wide delegation is set up. A scope that is in the code but not in
// the console fails at runtime with a bewildering 401, so the README quotes
// this block.
const (
	// ScopeGroupMember manages who is in a group, and nothing else about the
	// directory. It cannot create users, read mail or see a password.
	ScopeGroupMember = "https://www.googleapis.com/auth/admin.directory.group.member"
	// ScopeGroupRead reads the groups themselves, so a missing group can be
	// reported as missing rather than as a permission problem.
	ScopeGroupRead = "https://www.googleapis.com/auth/admin.directory.group.readonly"
	// ScopeContacts manages one mailbox's own address book.
	ScopeContacts = "https://www.googleapis.com/auth/contacts"
	// ScopeSheets writes the spreadsheet.
	ScopeSheets = "https://www.googleapis.com/auth/spreadsheets"
)

// DirectoryScopes are what the service account needs to keep the groups right.
var DirectoryScopes = []string{ScopeGroupMember, ScopeGroupRead}

// ContactsScopes are what it needs for one mailbox's address book.
var ContactsScopes = []string{ScopeContacts}

// SheetsScopes are what it needs for the spreadsheet.
var SheetsScopes = []string{ScopeSheets}

// Client mints access tokens for a service account and makes the calls.
//
// One Client serves every target. Tokens are cached per subject and scope
// set, because the registry impersonates a different mailbox for each address
// book it mirrors into and re-minting a token for every call would be both
// slow and rude to Google's rate limits.
type Client struct {
	sa   *config.ServiceAccount
	key  *rsa.PrivateKey
	http *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken
	// now is swapped out by the tests.
	now func() time.Time
}

type cachedToken struct {
	value   string
	expires time.Time
}

// New builds a Client from a service-account key.
func New(sa *config.ServiceAccount) (*Client, error) {
	key, err := parseKey(sa.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &Client{
		sa:     sa,
		key:    key,
		http:   &http.Client{Timeout: 30 * time.Second},
		tokens: map[string]cachedToken{},
		now:    time.Now,
	}, nil
}

// Account is the service account's own address, for the startup log and the
// sync page: nine times in ten a permission failure means somebody has not
// granted this address the scopes, and being able to read it off the page
// saves a trip to the key file.
func (c *Client) Account() string {
	if c == nil || c.sa == nil {
		return ""
	}
	return c.sa.ClientEmail
}

// parseKey reads the PEM private key out of a service-account key file.
func parseKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("the service account's private_key is not PEM; " +
			"check that the \\n escapes survived however it was passed in")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("the service account's key is %T, not RSA", key)
		}
		return rsaKey, nil
	}
	// Older key files carry a PKCS#1 key instead.
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse the service account's private key: %w", err)
	}
	return key, nil
}

// token returns an access token for impersonating subject with scopes.
//
// subject is a mailbox in the Workspace: group management runs as an
// administrator, and each address book runs as its own owner. A service
// account acting purely as itself cannot do either, however many scopes it
// holds — this is domain-wide delegation, and the subject is the whole point
// of it.
func (c *Client) token(ctx context.Context, subject string, scopes []string) (string, error) {
	key := subject + "|" + strings.Join(scopes, " ")

	c.mu.Lock()
	if t, ok := c.tokens[key]; ok && c.now().Before(t.expires) {
		c.mu.Unlock()
		return t.value, nil
	}
	c.mu.Unlock()

	assertion, err := c.assertion(subject, scopes)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sa.TokenURI,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ask Google for a token as %s: %w", subject, err)
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("read Google's token reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		return "", tokenError(subject, resp.StatusCode, body.Error, body.Description)
	}

	// Expire the cached copy a minute early, so a token cannot go stale
	// between the check and the call that uses it.
	life := time.Duration(body.ExpiresIn) * time.Second
	if life <= 0 {
		life = time.Hour
	}
	c.mu.Lock()
	c.tokens[key] = cachedToken{value: body.AccessToken, expires: c.now().Add(life - time.Minute)}
	c.mu.Unlock()
	return body.AccessToken, nil
}

// tokenError turns Google's terse OAuth complaints into something a board
// member reading the sync page can act on. "unauthorized_client" in
// particular means one specific, fixable thing, and saying so here saves an
// evening of searching.
func tokenError(subject string, status int, code, description string) error {
	switch code {
	case "unauthorized_client":
		return fmt.Errorf("Google will not let the service account act as %s: "+
			"add its client id and the scopes under Security → API controls → "+
			"Domain-wide delegation in the admin console (%s)", subject, description)
	case "invalid_grant":
		return fmt.Errorf("Google refused the impersonation of %s: usually the mailbox "+
			"does not exist, or the server's clock is wrong (%s)", subject, description)
	case "":
		return fmt.Errorf("Google returned %d asking for a token as %s", status, subject)
	}
	return fmt.Errorf("Google refused a token for %s: %s (%s)", subject, code, description)
}

// assertion builds the signed JWT that is exchanged for an access token.
func (c *Client) assertion(subject string, scopes []string) (string, error) {
	now := c.now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if c.sa.PrivateKeyID != "" {
		header["kid"] = c.sa.PrivateKeyID
	}
	claims := map[string]any{
		"iss":   c.sa.ClientEmail,
		"scope": strings.Join(scopes, " "),
		"aud":   c.sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	if subject != "" {
		claims["sub"] = subject
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64(headerJSON) + "." + b64(claimsJSON)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign the assertion: %w", err)
	}
	return signing + "." + b64(sig), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// call makes one authenticated JSON request and decodes the reply into out,
// which may be nil for a call whose answer is of no interest.
func (c *Client) call(ctx context.Context, subject string, scopes []string,
	method, endpoint string, body, out any) error {

	token, err := c.token(ctx, subject, scopes)
	if err != nil {
		return err
	}

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, short(endpoint), err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return fmt.Errorf("read Google's reply to %s: %w", short(endpoint), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp.StatusCode, buf.Bytes(), method, endpoint)
	}
	if out == nil || buf.Len() == 0 {
		return nil
	}
	if err := json.Unmarshal(buf.Bytes(), out); err != nil {
		return fmt.Errorf("read Google's reply to %s: %w", short(endpoint), err)
	}
	return nil
}

// Error is a refusal from a Google API, with the status kept separate so a
// caller can tell "there is no such thing" from "you may not".
type Error struct {
	Status  int
	Reason  string
	Message string
	Method  string
	URL     string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Google returned %d for %s %s", e.Status, e.Method, short(e.URL))
	}
	if e.Reason != "" && e.Reason != e.Message {
		return fmt.Sprintf("%s (%s)", e.Message, e.Reason)
	}
	return e.Message
}

// NotFound reports whether an error is Google saying the thing is not there.
func NotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Conflict reports whether an error is Google saying it is already there,
// which for an "add this address to this group" is a success in disguise.
func Conflict(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusConflict || e.Reason == "duplicate")
}

// Forbidden reports whether an error is a permission problem, which is nearly
// always a missing scope in the admin console rather than anything to do with
// the member being synchronised.
func Forbidden(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusForbidden || e.Status == http.StatusUnauthorized)
}

func apiError(status int, body []byte, method, endpoint string) error {
	var wrapper struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Errors  []struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"error"`
	}
	e := &Error{Status: status, Method: method, URL: endpoint}
	if err := json.Unmarshal(body, &wrapper); err == nil {
		e.Message = wrapper.Error.Message
		e.Reason = wrapper.Error.Status
		if len(wrapper.Error.Errors) > 0 && wrapper.Error.Errors[0].Reason != "" {
			e.Reason = wrapper.Error.Errors[0].Reason
		}
	}
	if e.Message == "" {
		// Not JSON, or JSON we do not recognise. A trimmed body beats nothing,
		// but a whole HTML error page on the sync log helps no one.
		e.Message = strings.TrimSpace(string(body))
		if len(e.Message) > 200 {
			e.Message = e.Message[:200] + "…"
		}
	}
	return e
}

// short trims the query string off an endpoint for an error message.
func short(endpoint string) string {
	if i := strings.IndexByte(endpoint, '?'); i > 0 {
		return endpoint[:i]
	}
	return endpoint
}
