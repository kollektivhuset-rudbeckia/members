package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
)

const testConfigYAML = `
site: {title: Test, language: sv, timezone: Europe/Stockholm}
membership: {fee_kr: 200, due_on: "03-31", grace_days: 14, new_member_days: 45}
groups:
  - {kind: bo, email: bomedlemmar@example.test}
  - {kind: van, email: friends@example.test}
`

type harness struct {
	server *Server
	store  *store.Store
	rt     config.Runtime
	cfg    *config.Config
	now    time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	cfg, err := config.Parse([]byte(testConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	rt := config.Runtime{
		BaseURL: "http://localhost:8080", DBPath: ":memory:",
		SessionSecret: []byte("test-secret-that-is-long-enough"),
		SessionMaxAge: time.Hour,
		Accounts: map[string]config.Role{
			"ny@example.test":        config.RoleIntake,
			"ekonomi@example.test":   config.RoleCashier,
			"styrelsen@example.test": config.RoleBoard,
		},
		Access: config.Access{PaymentRoles: map[config.Role]bool{config.RoleCashier: true}},
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A syncer with no Google client: it records nothing and reaches nothing,
	// which is exactly what a test wants and what an unconfigured deployment
	// looks like.
	syncer := sync.New(cfg, rt, st, nil, log)

	srv, err := New(cfg, rt, st, auth.New(rt), syncer, log)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, cfg.Location())
	srv.now = func() time.Time { return now }

	return &harness{server: srv, store: st, rt: rt, cfg: cfg, now: now}
}

// as returns a request cookie for a signed-in role.
func (h *harness) as(t *testing.T, role config.Role) *http.Cookie {
	t.Helper()
	var email string
	for address, r := range h.rt.Accounts {
		if r == role {
			email = address
		}
	}
	if email == "" {
		t.Fatalf("no account for role %q", role)
	}
	rec := httptest.NewRecorder()
	auth.New(h.rt).Issue(rec, email, "Test")
	for _, c := range rec.Result().Cookies() {
		if c.Name == "rb_session" {
			return c
		}
	}
	t.Fatal("no session cookie was issued")
	return nil
}

func (h *harness) do(t *testing.T, role config.Role, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if form == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if role != config.RoleNone {
		req.AddCookie(h.as(t, role))
	}
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	return rec
}

func (h *harness) member(t *testing.T, id, email string, kind config.Kind) store.Member {
	t.Helper()
	m := store.Member{
		ID: id, FirstName: "Anna", LastName: "Andersson", Email: email,
		Kind: kind, JoinedOn: h.now.AddDate(-3, 0, 0),
		CreatedAt: h.now.AddDate(-3, 0, 0), UpdatedAt: h.now.AddDate(-3, 0, 0),
	}
	if err := h.store.CreateMember(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEveryPageNeedsASignIn(t *testing.T) {
	h := newHarness(t)
	for _, page := range []string{"/", "/avgifter", "/andringar", "/synk", "/logg",
		"/medlem/ny", "/export.csv"} {
		rec := h.do(t, config.RoleNone, "GET", page, nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s: got %d, want a redirect to the sign-in", page, rec.Code)
		}
		if got := rec.Header().Get("Location"); !strings.HasPrefix(got, "/login") {
			t.Errorf("%s: redirected to %q, want /login", page, got)
		}
	}
}

// The permission matrix as the router actually enforces it. A button hidden
// in a template is a courtesy; this is the part that has to hold.
func TestPermissionsAreEnforcedByTheRouter(t *testing.T) {
	tests := []struct {
		name   string
		role   config.Role
		method string
		path   string
		form   url.Values
		want   int
	}{
		{"intake may not record a payment", config.RoleIntake, "POST",
			"/medlem/m1/betalning", url.Values{"ar": {"2026"}}, http.StatusForbidden},
		{"the board may not record a payment either", config.RoleBoard, "POST",
			"/medlem/m1/betalning", url.Values{"ar": {"2026"}}, http.StatusForbidden},
		{"the cashier may", config.RoleCashier, "POST",
			"/medlem/m1/betalning", url.Values{"ar": {"2026"}}, http.StatusSeeOther},

		{"intake may not approve", config.RoleIntake, "POST",
			"/andringar/p1/godkann", nil, http.StatusForbidden},
		{"the cashier may not approve", config.RoleCashier, "POST",
			"/andringar/p1/godkann", nil, http.StatusForbidden},

		{"intake may not sync by hand", config.RoleIntake, "POST", "/synk/kor", nil, http.StatusForbidden},
		{"the cashier may not sync by hand", config.RoleCashier, "POST", "/synk/kor", nil, http.StatusForbidden},

		{"intake may not read the audit trail", config.RoleIntake, "GET", "/logg", nil, http.StatusForbidden},
		{"the board may", config.RoleBoard, "GET", "/logg", nil, http.StatusOK},

		{"everybody may add", config.RoleIntake, "GET", "/medlem/ny", nil, http.StatusOK},
		{"everybody may see the register", config.RoleIntake, "GET", "/", nil, http.StatusOK},
		{"everybody may see the fees", config.RoleIntake, "GET", "/avgifter", nil, http.StatusOK},
		{"everybody may see the sync page", config.RoleIntake, "GET", "/synk", nil, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.member(t, "m1", "anna@example.test", config.KindBo)
			rec := h.do(t, tc.role, tc.method, tc.path, tc.form)
			if rec.Code != tc.want {
				t.Errorf("got %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestIntakeAddsAMemberOutright(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, config.RoleIntake, "POST", "/medlem/ny", url.Values{
		"fornamn": {"Bo"}, "efternamn": {"Bengtsson"},
		"epost": {"Bo.Bengtsson@Example.test"}, "typ": {"van"},
		"medlem_sedan": {"2026-05-01"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want a redirect\n%s", rec.Code, rec.Body.String())
	}
	m, err := h.store.MemberByEmail(context.Background(), "bo.bengtsson@example.test", h.cfg.Location())
	if err != nil {
		t.Fatalf("the member was not written: %v", err)
	}
	if m.Kind != config.KindVan {
		t.Errorf("kind: got %q, want van", m.Kind)
	}
	if m.CreatedBy != "ny@example.test" {
		t.Errorf("created by: got %q", m.CreatedBy)
	}
}

// The heart of what the house asked for: whoever answers the "I'd like to
// join" mail may write somebody down, but may not quietly change them.
func TestIntakesEditBecomesAProposalAndChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)

	rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1", url.Values{
		"fornamn": {"Annika"}, "efternamn": {"Andersson"},
		"epost": {"anna@example.test"}, "typ": {"van"},
		"medlem_sedan": {"2023-06-01"}, "anledning": {"bytte namn"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want a redirect\n%s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	m, err := h.store.Member(ctx, "m1", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if m.FirstName != "Anna" || m.Kind != config.KindBo {
		t.Errorf("the member was changed without the board: %+v", m)
	}

	pending, err := h.store.PendingProposals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d proposals, want 1", len(pending))
	}
	if pending[0].Kind != store.ProposeUpdate {
		t.Errorf("kind: got %q, want update", pending[0].Kind)
	}
	if pending[0].After.FirstName != "Annika" {
		t.Errorf("the proposal did not carry the change: %+v", pending[0].After)
	}
	if pending[0].Reason != "bytte namn" {
		t.Errorf("reason: got %q", pending[0].Reason)
	}
}

func TestIntakesDeleteBecomesAProposalAndTheMemberSurvives(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)

	rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1/ta-bort",
		url.Values{"anledning": {"ångrade sig"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want a redirect", rec.Code)
	}

	ctx := context.Background()
	if _, err := h.store.Member(ctx, "m1", h.cfg.Location()); err != nil {
		t.Fatalf("the member was removed without the board: %v", err)
	}
	pending, err := h.store.PendingProposals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Kind != store.ProposeDelete {
		t.Fatalf("got %+v, want one delete proposal", pending)
	}
}

func TestTheBoardEditsAndRemovesOutright(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)
	ctx := context.Background()

	rec := h.do(t, config.RoleBoard, "POST", "/medlem/m1", url.Values{
		"fornamn": {"Annika"}, "efternamn": {"Andersson"},
		"epost": {"anna@example.test"}, "typ": {"bo"}, "medlem_sedan": {"2023-06-01"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: got %d\n%s", rec.Code, rec.Body.String())
	}
	m, err := h.store.Member(ctx, "m1", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if m.FirstName != "Annika" {
		t.Errorf("the board's edit did not take: %+v", m)
	}
	if n, _ := h.store.CountPending(ctx); n != 0 {
		t.Errorf("the board's own edit should not need approving: %d pending", n)
	}

	if rec := h.do(t, config.RoleBoard, "POST", "/medlem/m1/ta-bort", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: got %d", rec.Code)
	}
	if _, err := h.store.Member(ctx, "m1", h.cfg.Location()); err == nil {
		t.Error("the board's removal did not take")
	}
}

func TestApprovingAProposalAppliesIt(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)
	ctx := context.Background()

	h.do(t, config.RoleIntake, "POST", "/medlem/m1", url.Values{
		"fornamn": {"Anna"}, "efternamn": {"Andersson"},
		"epost": {"anna@example.test"}, "typ": {"van"}, "medlem_sedan": {"2023-06-01"},
	})
	pending, _ := h.store.PendingProposals(ctx)
	if len(pending) != 1 {
		t.Fatalf("setup: got %d proposals", len(pending))
	}

	rec := h.do(t, config.RoleBoard, "POST", "/andringar/"+pending[0].ID+"/godkann",
		url.Values{"kommentar": {"ok"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("approve: got %d\n%s", rec.Code, rec.Body.String())
	}
	m, err := h.store.Member(ctx, "m1", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != config.KindVan {
		t.Errorf("the approved change did not take: %+v", m)
	}
}

// Approving a proposal written against a version somebody has since replaced
// would silently undo their work. It has to be retired instead.
func TestAProposalOvertakenByAnEditIsRetiredRatherThanApplied(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)
	ctx := context.Background()

	// Intake proposes a change to the telephone number.
	h.do(t, config.RoleIntake, "POST", "/medlem/m1", url.Values{
		"fornamn": {"Anna"}, "efternamn": {"Andersson"}, "telefon": {"070-1"},
		"epost": {"anna@example.test"}, "typ": {"bo"}, "medlem_sedan": {"2023-06-01"},
	})
	pending, _ := h.store.PendingProposals(ctx)
	if len(pending) != 1 {
		t.Fatalf("setup: got %d proposals", len(pending))
	}

	// Meanwhile the cashier corrects the address.
	h.do(t, config.RoleCashier, "POST", "/medlem/m1", url.Values{
		"fornamn": {"Anna"}, "efternamn": {"Andersson"},
		"epost": {"anna.andersson@example.test"}, "typ": {"bo"}, "medlem_sedan": {"2023-06-01"},
	})

	// The board's approval must not put the old address back.
	h.do(t, config.RoleBoard, "POST", "/andringar/"+pending[0].ID+"/godkann", url.Values{})

	m, err := h.store.Member(ctx, "m1", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if m.Email != "anna.andersson@example.test" {
		t.Errorf("the cashier's correction was undone: got %q", m.Email)
	}
	p, err := h.store.Proposal(ctx, pending[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status == store.Approved {
		t.Error("a proposal written against a replaced version was applied anyway")
	}
}

func TestTheCashierRecordsAndWithdrawsAFee(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)
	ctx := context.Background()

	rec := h.do(t, config.RoleCashier, "POST", "/medlem/m1/betalning", url.Values{
		"ar": {"2026"}, "belopp": {"200"}, "datum": {"2026-03-02"}, "satt": {"Bankgiro"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("record: got %d\n%s", rec.Code, rec.Body.String())
	}
	p, err := h.store.Payment(ctx, "m1", 2026, h.cfg.Location())
	if err != nil {
		t.Fatalf("the payment was not written: %v", err)
	}
	if p.AmountKr != 200 || p.RegisteredBy != "ekonomi@example.test" {
		t.Errorf("payment: %+v", p)
	}

	if rec := h.do(t, config.RoleCashier, "POST", "/medlem/m1/angra-betalning",
		url.Values{"ar": {"2026"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("withdraw: got %d", rec.Code)
	}
	if _, err := h.store.Payment(ctx, "m1", 2026, h.cfg.Location()); err == nil {
		t.Error("the payment was not withdrawn")
	}
}

// A Swedish keyboard writes a decimal point as a comma, and the register
// counts in whole kronor. "200,50" is 200 kr, not a rejection.
func TestAnAmountWithACommaIsRead(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)

	h.do(t, config.RoleCashier, "POST", "/medlem/m1/betalning", url.Values{
		"ar": {"2026"}, "belopp": {"200,50"},
	})
	p, err := h.store.Payment(context.Background(), "m1", 2026, h.cfg.Location())
	if err != nil {
		t.Fatalf("the payment was not written: %v", err)
	}
	if p.AmountKr != 200 {
		t.Errorf("got %d kr, want 200", p.AmountKr)
	}
}

func TestABadFormComesBackFilledIn(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, config.RoleIntake, "POST", "/medlem/ny", url.Values{
		"fornamn": {"Bo"}, "epost": {"not-an-address"}, "typ": {"bo"},
		"medlem_sedan": {"2026-05-01"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "not-an-address") {
		t.Error("the rejected form came back empty; the typing should survive")
	}
	if !strings.Contains(body, "e-postadress") {
		t.Error("no complaint about the address was shown")
	}
}

func TestADuplicateAddressIsRefusedWithAnExplanation(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)

	rec := h.do(t, config.RoleIntake, "POST", "/medlem/ny", url.Values{
		"fornamn": {"Annika"}, "efternamn": {"Andersson"},
		"epost": {"ANNA@example.test"}, "typ": {"bo"}, "medlem_sedan": {"2026-05-01"},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "finns redan") {
		t.Error("the page did not say the address was taken")
	}
}

func TestAMemberThatDoesNotExistIsANotFoundPage(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, config.RoleBoard, "GET", "/medlem/nobody", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

func TestTheRegisterFiltersAndSorts(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)
	h.member(t, "m2", "bo@example.test", config.KindVan)

	rec := h.do(t, config.RoleBoard, "GET", "/?typ=van", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "anna@example.test") {
		t.Error("the resident showed up in a friends-only filter")
	}
	if !strings.Contains(body, "bo@example.test") {
		t.Error("the friend was filtered out of a friends-only filter")
	}

	// A sort key that does not exist must not break the page.
	if rec := h.do(t, config.RoleBoard, "GET", "/?ordna=../../etc/passwd", nil); rec.Code != http.StatusOK {
		t.Errorf("a nonsense sort key gave %d", rec.Code)
	}
}

func TestTheExportIsASemicolonCSVWithABOM(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)

	rec := h.do(t, config.RoleBoard, "GET", "/export.csv", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	body := rec.Body.String()
	// Excel needs the byte-order mark to open UTF-8 without mangling every å,
	// and a Swedish Excel reads a comma as a decimal point.
	if !strings.HasPrefix(body, "\xEF\xBB\xBF") {
		t.Error("no byte-order mark; Excel will mangle the Swedish letters")
	}
	if !strings.Contains(body, ";") {
		t.Error("not semicolon separated")
	}
	if !strings.Contains(body, "anna@example.test") {
		t.Error("the member is missing from the export")
	}
}

func TestSignInRedirectsBackToWhereYouWereGoing(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, config.RoleNone, "GET", "/avgifter?ar=2025", nil)
	got := rec.Header().Get("Location")
	if !strings.Contains(got, url.QueryEscape("/avgifter?ar=2025")) {
		t.Errorf("the destination was lost: %q", got)
	}
}

// A redirect target is somewhere an attacker would love to put their own
// address, so it never leaves this site.
func TestARedirectCannotLeaveTheSite(t *testing.T) {
	for _, next := range []string{"https://evil.test", "//evil.test", "/../..", ""} {
		if got := safeNext(next); !strings.HasPrefix(got, "/") || strings.HasPrefix(got, "//") {
			t.Errorf("safeNext(%q) = %q", next, got)
		}
	}
	if got := safeNext("/avgifter?ar=2025"); got != "/avgifter?ar=2025" {
		t.Errorf("an ordinary destination was mangled: %q", got)
	}
}

func TestTheDemoSignInIsRefusedWhenTheRegisterIsReal(t *testing.T) {
	h := newHarness(t) // rt.Demo is false
	rec := h.do(t, config.RoleNone, "POST", "/demo-inloggning",
		url.Values{"roll": {"board"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 — the demo door must not exist in earnest", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "rb_session" && c.Value != "" {
			t.Fatal("a session was issued by the demo door on a real deployment")
		}
	}
}

func TestHealthAndStaticFilesNeedNoSignIn(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(t, config.RoleNone, "GET", "/healthz", nil); rec.Code != http.StatusOK {
		t.Errorf("healthz: got %d", rec.Code)
	}
	rec := h.do(t, config.RoleNone, "GET", "/static/app.css", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("stylesheet: got %d", rec.Code)
	}
}

func TestSecurityHeadersAreSet(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, config.RoleBoard, "GET", "/", nil)
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s: got %q, want %q", header, got, want)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("the register can be framed: %q", csp)
	}
	// The sign-in posts to Google, and nothing else may be posted to.
	if !strings.Contains(csp, "form-action 'self' https://accounts.google.com") {
		t.Errorf("form-action is wrong: %q", csp)
	}
}

// A session names an account; what that account may do is looked up afresh on
// every request. Taking somebody out of the configuration has to lock them
// out at once, not when their cookie happens to expire.
func TestARoleRevokedInTheConfigurationLocksTheSessionOut(t *testing.T) {
	h := newHarness(t)
	cookie := h.as(t, config.RoleCashier)

	delete(h.rt.Accounts, "ekonomi@example.test")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(h.cfg, h.rt, h.store, auth.New(h.rt),
		sync.New(h.cfg, h.rt, h.store, nil, log), log)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("got %d, want a redirect to the sign-in", rec.Code)
	}
}

// The sync button used to run the whole pass inside the request. A first pass
// over three address books is several hundred calls to Google and minutes of
// waiting, so the request timed out and the person pressed it again. It has
// to start the work and say so.
func TestSyncStartsInTheBackgroundAndSaysSo(t *testing.T) {
	h := newHarness(t)
	// Without a Google client there is nothing to start, and the page should
	// say that rather than pretend.
	rec := h.do(t, config.RoleBoard, "POST", "/synk/kor", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want a redirect", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/synk" {
		t.Errorf("redirected to %q, want /synk", got)
	}
}

// A hand-typed target must not send the reconciler after something that is
// not one of ours.
func TestSyncRefusesATargetItDoesNotKnow(t *testing.T) {
	h := newHarness(t)
	for _, target := range []string{"group:evil@example.test", "../../etc", "sheet2"} {
		rec := h.do(t, config.RoleBoard, "POST", "/synk/kor", url.Values{"mal": {target}})
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%q: got %d", target, rec.Code)
		}
	}
	// And the ones from the configuration are accepted.
	for _, target := range h.cfg.Targets() {
		if !h.server.knownTarget(target) {
			t.Errorf("%q is configured but not recognised", target)
		}
	}
}

func TestOnlyTheBoardCanStartASync(t *testing.T) {
	h := newHarness(t)
	for _, role := range []config.Role{config.RoleIntake, config.RoleCashier} {
		rec := h.do(t, role, "POST", "/synk/kor", url.Values{})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", role, rec.Code)
		}
	}
}

// A register of a few hundred is a long table. It pages — and the paging must
// not quietly change what a filter means.
func TestTheRegisterPages(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 60; i++ {
		h.member(t, fmt.Sprintf("m%02d", i), fmt.Sprintf("m%02d@example.test", i), config.KindBo)
	}

	first := h.do(t, config.RoleBoard, "GET", "/", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("got %d", first.Code)
	}
	body := first.Body.String()
	if !strings.Contains(body, "Visar 1–50 av 60") {
		t.Error("the pager does not say which rows are on screen")
	}
	if strings.Contains(body, "m59@example.test") {
		t.Error("row 60 is on the first page of 50")
	}

	second := h.do(t, config.RoleBoard, "GET", "/?sida=2", nil).Body.String()
	if !strings.Contains(second, "m59@example.test") {
		t.Error("row 60 is not on the second page either")
	}
	if strings.Contains(second, "m00@example.test") {
		t.Error("the second page still holds the first page's rows")
	}

	// Asking for a page past the end lands on the last one rather than on
	// nothing at all.
	beyond := h.do(t, config.RoleBoard, "GET", "/?sida=99", nil).Body.String()
	if !strings.Contains(beyond, "m59@example.test") {
		t.Error("a page past the end should show the last page")
	}

	// And everything on one page when asked.
	all := h.do(t, config.RoleBoard, "GET", "/?antal=0", nil).Body.String()
	if !strings.Contains(all, "m00@example.test") || !strings.Contains(all, "m59@example.test") {
		t.Error("antal=0 did not put the whole register on one page")
	}
}

// The search box filters in the browser, which is fast and right — but only
// while the browser can see every row. Filtering the visible page and quietly
// hiding the rest would be worse than not filtering at all.
func TestTheBrowserOnlyFiltersWhenItCanSeeEverything(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 60; i++ {
		h.member(t, fmt.Sprintf("m%02d", i), fmt.Sprintf("m%02d@example.test", i), config.KindBo)
	}

	paged := h.do(t, config.RoleBoard, "GET", "/", nil).Body.String()
	if strings.Contains(paged, "data-table-filter") {
		t.Error("the browser would filter only the visible page")
	}
	if !strings.Contains(paged, "hela registret") {
		t.Error("nothing tells the reader that the search covers everything")
	}

	whole := h.do(t, config.RoleBoard, "GET", "/?antal=0", nil).Body.String()
	if !strings.Contains(whole, "data-table-filter") {
		t.Error("with every row on the page the browser should filter")
	}
}

// Changing a filter or the sort has to start again at the first page: page 3
// of the old view is meaningless in the new one.
func TestFilteringAndSortingReturnToTheFirstPage(t *testing.T) {
	q := tableQuery{Sort: "name", Page: 3, PerPage: 50}
	if link := q.SortLink("/", "email"); strings.Contains(link, "sida") {
		t.Errorf("a sort kept the page number: %s", link)
	}
	if link := q.PerPageLink("/", 100); strings.Contains(link, "sida") {
		t.Errorf("changing the page size kept the page number: %s", link)
	}
	// But a page link keeps the filter and the sort. The default sort is
	// deliberately left out of the address, so use another one.
	q.Filter.Kind = config.KindVan
	q.Sort = "tenure"
	link := q.PageLink("/", 2)
	for _, want := range []string{"typ=van", "ordna=tenure", "sida=2"} {
		if !strings.Contains(link, want) {
			t.Errorf("the page link lost %q: %s", want, link)
		}
	}
}

// The spreadsheet is the one thing here that lives somewhere else.
func TestTheSpreadsheetIsLinkedFromEveryPage(t *testing.T) {
	h := newHarness(t)
	if h.server.sheetURL() != "" {
		t.Fatal("the test configuration should have no spreadsheet")
	}
	if strings.Contains(h.do(t, config.RoleBoard, "GET", "/", nil).Body.String(), "docs.google.com") {
		t.Error("a link appeared with no spreadsheet configured")
	}

	h.cfg.Sheet.ID = "abc123"
	page := h.do(t, config.RoleBoard, "GET", "/", nil).Body.String()
	if !strings.Contains(page, "https://docs.google.com/spreadsheets/d/abc123/edit") {
		t.Error("the spreadsheet is not linked from the register")
	}
	if !strings.Contains(h.do(t, config.RoleCashier, "GET", "/avgifter", nil).Body.String(), "abc123") {
		t.Error("the cashier cannot reach the spreadsheet from the fees page")
	}
}
