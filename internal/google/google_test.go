package google

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// A refusal from Google is the text a board member reads on the sync page, so
// what it says matters as much as that it is an error at all.
func TestAnAPIRefusalKeepsGooglesOwnComplaint(t *testing.T) {
	body := []byte(`{"error":{"code":404,"message":"Resource Not Found: groupKey.",` +
		`"status":"NOT_FOUND","errors":[{"reason":"notFound","message":"Not Found"}]}}`)
	err := apiError(http.StatusNotFound, body, "GET", "https://admin.googleapis.com/x?y=1")

	if !NotFound(err) {
		t.Error("a 404 was not recognised as a missing thing")
	}
	if got := err.Error(); got != "Resource Not Found: groupKey. (notFound)" {
		t.Errorf("message: %q", got)
	}
}

func TestARefusalThatIsNotJSONIsStillReadable(t *testing.T) {
	err := apiError(http.StatusBadGateway, []byte("<html><body>502 Bad Gateway</body></html>"),
		"POST", "https://people.googleapis.com/v1/people:createContact")
	if err.Error() == "" {
		t.Error("an HTML error page produced an empty message")
	}
	if NotFound(err) || Conflict(err) {
		t.Error("a 502 was mistaken for something else")
	}
}

func TestAVeryLongRefusalIsTrimmed(t *testing.T) {
	long := make([]byte, 4000)
	for i := range long {
		long[i] = 'x'
	}
	err := apiError(http.StatusInternalServerError, long, "GET", "https://example.test")
	if len(err.Error()) > 400 {
		t.Errorf("a whole error page reached the sync log: %d characters", len(err.Error()))
	}
}

// "This address is already in the group" is a success in disguise: the
// register's job is to make the group match, and a group that already matches
// has nothing to complain about.
func TestAlreadyThereIsRecognisedAsAConflict(t *testing.T) {
	byStatus := apiError(http.StatusConflict,
		[]byte(`{"error":{"code":409,"message":"Member already exists."}}`), "POST", "x")
	if !Conflict(byStatus) {
		t.Error("a 409 was not recognised as a conflict")
	}
	byReason := apiError(http.StatusBadRequest,
		[]byte(`{"error":{"code":400,"message":"dup","errors":[{"reason":"duplicate"}]}}`), "POST", "x")
	if !Conflict(byReason) {
		t.Error("a duplicate reason was not recognised as a conflict")
	}
}

// Nine permission failures in ten mean a scope nobody granted in the admin
// console, which is a different conversation from a member being wrong.
func TestForbiddenCoversBothWaysGoogleSaysNo(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusUnauthorized} {
		err := apiError(status, []byte(`{"error":{"message":"Not Authorized"}}`), "GET", "x")
		if !Forbidden(err) {
			t.Errorf("%d was not recognised as a permission problem", status)
		}
	}
	if Forbidden(apiError(http.StatusNotFound, nil, "GET", "x")) {
		t.Error("a 404 was mistaken for a permission problem")
	}
}

func TestTheErrorHelpersIgnoreUnrelatedErrors(t *testing.T) {
	plain := errors.New("the network went away")
	if NotFound(plain) || Conflict(plain) || Forbidden(plain) {
		t.Error("a plain error was classified as one of Google's")
	}
	if NotFound(nil) || Conflict(nil) || Forbidden(nil) {
		t.Error("nil was classified as an error")
	}
}

// "unauthorized_client" means one specific, fixable thing, and saying so
// saves an evening of searching.
func TestTokenRefusalsExplainThemselves(t *testing.T) {
	err := tokenError("styrelsen@example.test", 400, "unauthorized_client", "Client is unauthorized")
	msg := err.Error()
	for _, want := range []string{"Domain-wide delegation", "styrelsen@example.test"} {
		if !contains(msg, want) {
			t.Errorf("the message should mention %q: %s", want, msg)
		}
	}

	clock := tokenError("ny@example.test", 400, "invalid_grant", "Invalid JWT")
	if !contains(clock.Error(), "clock") {
		t.Errorf("invalid_grant should mention the clock: %s", clock)
	}
}

func TestPersonAccessorsPickTheFirstUsefulValue(t *testing.T) {
	p := Person{
		ResourceName: "people/c123",
		Names: []Name{
			{}, // an empty name block, which Google does return
			{DisplayName: "Anna Andersson", GivenName: "Anna", FamilyName: "Andersson"},
		},
		Emails:      []Email{{Value: "  Anna.Andersson@Example.TEST "}},
		Phones:      []Phone{{Value: ""}, {Value: "070-1"}},
		Biographies: []Biography{{Value: "Bomedlem sedan 2020-01-01"}},
	}
	if got := p.PrimaryEmail(); got != "anna.andersson@example.test" {
		t.Errorf("email: got %q", got)
	}
	if got := p.DisplayName(); got != "Anna Andersson" {
		t.Errorf("name: got %q", got)
	}
	if got := p.PrimaryPhone(); got != "070-1" {
		t.Errorf("phone: got %q", got)
	}
	if got := p.Notes(); got != "Bomedlem sedan 2020-01-01" {
		t.Errorf("notes: got %q", got)
	}
	if got := p.ID(); got != "c123" {
		t.Errorf("id: got %q", got)
	}

	empty := Person{}
	if empty.PrimaryEmail() != "" || empty.DisplayName() != "" || empty.PrimaryPhone() != "" {
		t.Error("an empty card produced values out of nowhere")
	}
}

// "Medlemmar 2026!A1" is not the same range as "'Medlemmar 2026'!A1", and the
// unquoted one is an error rather than a different answer.
func TestATabNameWithASpaceIsQuoted(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Medlemmar", "Medlemmar"},
		{"Medlemmar 2026", "'Medlemmar 2026'"},
		{"Anna's flik", "'Anna''s flik'"},
	}
	for _, tc := range tests {
		if got := quoteTab(tc.in); got != tc.want {
			t.Errorf("quoteTab(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAPrivateKeyThatIsNotPEMSaysSo(t *testing.T) {
	_, err := parseKey("-----BEGIN PRIVATE KEY-----\\nnot really\\n-----END PRIVATE KEY-----")
	if err == nil {
		t.Fatal("nonsense was accepted as a private key")
	}
	// The commonest cause is the \n escapes not surviving however the key was
	// passed in, and saying so is the difference between a five-minute fix
	// and an afternoon.
	if !contains(err.Error(), "n escapes") {
		t.Errorf("the message should hint at the usual cause: %v", err)
	}
}

func TestAContactGroupIDIsTheBareID(t *testing.T) {
	g := ContactGroup{ResourceName: "contactGroups/abc123"}
	if got := g.ID(); got != "abc123" {
		t.Errorf("got %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// The scopes have to be pasted into the Workspace admin console by hand, so
// the documentation quotes them. A scope that is in the code but not in the
// console fails at runtime with a bewildering 401 — and a scope the setup
// guide gets wrong sends somebody to look for the fault in the wrong place
// entirely. This keeps the three lists honest.
func TestTheDocumentationQuotesTheRealScopes(t *testing.T) {
	want := map[string]bool{}
	for _, group := range [][]string{DirectoryScopes, ContactsScopes, SheetsScopes} {
		for _, scope := range group {
			want[scope] = true
		}
	}

	for _, path := range []string{"../../README.md", "../../docs/google-workspace.md"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// The README abbreviates the shared prefix; expand it before matching.
		text := strings.ReplaceAll(string(raw), ".../auth/", "https://www.googleapis.com/auth/")

		found := map[string]bool{}
		for _, scope := range scopePattern.FindAllString(text, -1) {
			found[scope] = true
		}
		for scope := range want {
			if !found[scope] {
				t.Errorf("%s does not mention the scope %s, which the code asks Google for",
					path, scope)
			}
		}
		for scope := range found {
			if !want[scope] {
				t.Errorf("%s tells the reader to grant %s, which the code never uses",
					path, scope)
			}
		}
	}
}

var scopePattern = regexp.MustCompile(`https://www\.googleapis\.com/auth/[a-z.]+`)
