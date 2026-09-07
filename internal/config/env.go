package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Role is what an account signed in with Google may do. There are three, and
// they map to the three shared mailboxes the association already has.
type Role string

const (
	// RoleNone has not signed in.
	RoleNone Role = ""
	// RoleIntake is ny@ — whoever answers the "I'd like to join" mail. They
	// may write down a new member on the spot, but a change to somebody who
	// is already in the register, or removing them, waits for the board.
	RoleIntake Role = "intake"
	// RoleCashier is ekonomi@ — the only role that may say whether a fee has
	// been paid, because they are the ones looking at the bank statement.
	RoleCashier Role = "cashier"
	// RoleBoard is styrelsen@ — the association's own decision-making body.
	// It edits and removes directly, and it is what approves everybody else's
	// proposals.
	RoleBoard Role = "board"
)

// Roles are the roles that can sign in, in order of increasing authority.
var Roles = []Role{RoleIntake, RoleCashier, RoleBoard}

// LoggedIn reports whether the role got past the front door.
func (r Role) LoggedIn() bool { return r == RoleIntake || r == RoleCashier || r == RoleBoard }

// Board reports whether the role is the board itself.
func (r Role) Board() bool { return r == RoleBoard }

// Label is the role's name in Swedish, for the log and for the audit trail.
// What a person reads on a page comes from the catalogue instead.
func (r Role) Label() string {
	switch r {
	case RoleIntake:
		return "ny"
	case RoleCashier:
		return "ekonomi"
	case RoleBoard:
		return "styrelsen"
	}
	return "okänd"
}

// Permission is something a signed-in account may or may not do.
type Permission string

const (
	// PermView reads the register. Every role has it.
	PermView Permission = "view"
	// PermAdd writes down a new member. Every role has it: the point of the
	// register is that somebody can be added the moment they show interest.
	PermAdd Permission = "add"
	// PermEdit changes an existing member's details without asking anybody.
	PermEdit Permission = "edit"
	// PermDelete removes a member outright.
	PermDelete Permission = "delete"
	// PermPropose asks the board to make a change or a removal.
	PermPropose Permission = "propose"
	// PermApprove decides on somebody else's proposal.
	PermApprove Permission = "approve"
	// PermPay records or withdraws a payment.
	PermPay Permission = "pay"
	// PermSync starts a reconciliation by hand.
	PermSync Permission = "sync"
	// PermPipeline sees and works the candidate board.
	//
	// It is the interview team's own working notes about people who have not
	// agreed to anything yet — what they said about themselves, who is
	// looking after them, whether they were turned down. That is ny@'s
	// business and the board's, and nobody else's; the cashier has no reason
	// to read it and every reason not to have to.
	PermPipeline Permission = "pipeline"
	// PermPipelineEdit works the candidate board: moves cards between
	// columns, edits them, welcomes somebody, throws a card away.
	//
	// Seeing the board and working it are deliberately separate. The
	// interview team runs the pipeline — it is their process, their notes and
	// their judgement about who is where — and the board looking over their
	// shoulder is welcome, whereas the board quietly moving somebody out of
	// "Bokad intervju" is not. Nobody should be able to change a column that
	// somebody else is answerable for.
	PermPipelineEdit Permission = "pipeline.edit"
)

// Access is the permission matrix, resolved once at startup so that a page, a
// handler and the README cannot drift apart.
//
// The shape of it is the association's own division of labour:
//
//	                  ny      ekonomi   styrelsen
//	see the register   ✓         ✓          ✓
//	add a member       ✓         ✓          ✓
//	change one       propose     ✓          ✓
//	remove one       propose  propose       ✓
//	fee paid or not    ✗         ✓          ✗ (see PaymentRoles)
//	decide proposals   ✗         ✗          ✓
//	sync by hand       ✗         ✗          ✓
//	see the candidates ✓         ✗          ✓
//	work the board     ✓         ✗          ✗
type Access struct {
	// PaymentRoles are the roles that may touch a payment. The house asked
	// for this to be the cashier alone — they are the one with the bank
	// statement open — so that is the default. A house that wants the board
	// to be able to fix a slip sets PAYMENT_ROLES=cashier,board.
	PaymentRoles map[Role]bool
}

// May reports whether a role holds a permission.
func (a Access) May(r Role, p Permission) bool {
	if !r.LoggedIn() {
		return false
	}
	switch p {
	case PermView, PermAdd:
		return true
	case PermPay:
		return a.PaymentRoles[r]
	case PermEdit:
		return r == RoleBoard || r == RoleCashier
	case PermPipeline:
		return r == RoleIntake || r == RoleBoard
	case PermPipelineEdit:
		return r == RoleIntake
	case PermDelete, PermApprove, PermSync:
		return r == RoleBoard
	case PermPropose:
		// Anybody who cannot do a thing outright may ask for it instead.
		return r != RoleBoard
	}
	return false
}

// NeedsApproval reports whether this role's change to an existing member has
// to go past the board before it takes effect.
func (a Access) NeedsApproval(r Role, p Permission) bool {
	return a.May(r, PermPropose) && !a.May(r, p)
}

// Runtime holds the settings that come from the environment rather than YAML,
// because they are secrets or deployment specific.
type Runtime struct {
	ListenAddr    string
	ConfigPath    string
	DBPath        string
	BaseURL       string
	SessionSecret []byte
	SessionMaxAge time.Duration
	TrustProxy    bool
	Access        Access

	// Google is how the registry signs people in and how it reaches Workspace.
	Google GoogleSettings

	// Accounts maps a Workspace address to the role it signs in as. Nothing
	// outside this map gets through the front door, whatever domain it is on.
	Accounts map[string]Role

	// Demo runs a throwaway instance: no Google at all, made-up members, a
	// banner on every page and a sign-in page that just asks which of the
	// three roles you would like to be. Never enable it on a real deployment.
	Demo bool
}

// GoogleSettings is everything needed to talk to Google: the OAuth client
// that signs people in, and the service account that does the synchronising.
type GoogleSettings struct {
	// ClientID and ClientSecret are the OAuth 2.0 web client's credentials,
	// used for the sign-in flow.
	ClientID     string
	ClientSecret string
	// HostedDomain is the Workspace domain a sign-in must come from. It is
	// passed to Google as the `hd` parameter *and* checked again on the token
	// we get back: the parameter is a hint to the account chooser, not a
	// guarantee, so trusting it alone would be a hole.
	HostedDomain string

	// ServiceAccount is the JSON key of the service account that manages
	// groups, contacts and the spreadsheet.
	ServiceAccount *ServiceAccount
	// AdminSubject is the Workspace administrator the service account
	// impersonates for the Directory API. Group management is an admin
	// operation; a service account cannot do it as itself, however many
	// scopes it has been granted.
	AdminSubject string
}

// SignInReady reports whether Google sign-in is configured.
func (g GoogleSettings) SignInReady() bool { return g.ClientID != "" && g.ClientSecret != "" }

// SyncReady reports whether the service account can reach Workspace.
func (g GoogleSettings) SyncReady() bool { return g.ServiceAccount != nil }

// ServiceAccount is the useful half of a Google service-account key file.
type ServiceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// LoadRuntime reads deployment settings from the environment.
//
// Demo mode is the exception to everything below: it needs no credentials at
// all, so somebody who has just cloned the repository can start the registry
// and click around in it.
func LoadRuntime() (Runtime, error) {
	demo := envBool("DEMO", false)
	rt := Runtime{
		Demo:          demo,
		ListenAddr:    env("LISTEN_ADDR", ":8080"),
		ConfigPath:    env("CONFIG_PATH", "config.yaml"),
		DBPath:        env("DB_PATH", "data/members.db"),
		BaseURL:       strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		SessionMaxAge: time.Duration(envInt("SESSION_HOURS", 12)) * time.Hour,
		TrustProxy:    envBool("TRUST_PROXY", true),
		Google: GoogleSettings{
			ClientID:     strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")),
			ClientSecret: strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET")),
			HostedDomain: strings.ToLower(strings.TrimSpace(env("GOOGLE_HOSTED_DOMAIN", "rudbeckia.nu"))),
			AdminSubject: strings.ToLower(strings.TrimSpace(os.Getenv("GOOGLE_ADMIN_SUBJECT"))),
		},
	}

	accounts, err := readAccounts()
	if err != nil {
		return rt, err
	}
	rt.Accounts = accounts

	roles, err := readPaymentRoles()
	if err != nil {
		return rt, err
	}
	rt.Access = Access{PaymentRoles: roles}

	if demo {
		// A demo must never hold a credential or reach a real Workspace: the
		// sign-in page there hands out roles for the asking.
		rt.Google = GoogleSettings{HostedDomain: "example.test"}
		rt.SessionSecret = demoSecret()
		return rt, nil
	}

	if !rt.Google.SignInReady() {
		return rt, errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET must be set " +
			"(or run with -demo to try the registry out)")
	}
	if rt.Google.HostedDomain == "" {
		return rt, errors.New("GOOGLE_HOSTED_DOMAIN must name the Workspace domain, e.g. rudbeckia.nu")
	}
	if sa, err := readServiceAccount(); err != nil {
		return rt, err
	} else if sa != nil {
		rt.Google.ServiceAccount = sa
		if rt.Google.AdminSubject == "" {
			return rt, errors.New("GOOGLE_ADMIN_SUBJECT must name the Workspace administrator " +
				"the service account impersonates; managing a group is an admin operation")
		}
	}

	// A stable secret keeps sessions alive across a restart. Without one the
	// registry makes a fresh secret per process, which is safe but signs
	// everybody out on every deploy.
	if s := strings.TrimSpace(os.Getenv("SESSION_SECRET")); s != "" {
		sum := sha256.Sum256([]byte(s))
		rt.SessionSecret = sum[:]
	} else {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return rt, fmt.Errorf("crypto/rand unavailable: %w", err)
		}
		rt.SessionSecret = b
	}
	return rt, nil
}

// RedirectURI is where Google sends the browser back after a sign-in. It has
// to match the OAuth client's registered redirect exactly, so it is derived
// from BASE_URL rather than configured separately and left to drift.
func (r Runtime) RedirectURI() string { return r.BaseURL + "/oauth2/callback" }

// Secure reports whether cookies should be marked Secure, which is right as
// soon as the site is served over HTTPS.
func (r Runtime) Secure() bool { return strings.HasPrefix(r.BaseURL, "https://") }

// Role returns the role an address signs in as, or RoleNone for everybody
// else. Comparison is case-insensitive; Google hands the address back in
// whatever case the account was created with.
func (r Runtime) Role(email string) Role {
	return r.Accounts[strings.ToLower(strings.TrimSpace(email))]
}

// AccountFor is the address that holds a role, for the pages that have to say
// "ask styrelsen@rudbeckia.nu" rather than "ask the board".
func (r Runtime) AccountFor(role Role) string {
	for email, have := range r.Accounts {
		if have == role {
			return email
		}
	}
	return ""
}

// readAccounts builds the address-to-role map. The three defaults are the
// association's own mailboxes; a deployment elsewhere overrides them.
func readAccounts() (map[string]Role, error) {
	defaults := map[Role]string{
		RoleIntake:  "ny@rudbeckia.nu",
		RoleCashier: "ekonomi@rudbeckia.nu",
		RoleBoard:   "styrelsen@rudbeckia.nu",
	}
	vars := map[Role]string{
		RoleIntake:  "ACCOUNT_INTAKE",
		RoleCashier: "ACCOUNT_CASHIER",
		RoleBoard:   "ACCOUNT_BOARD",
	}
	out := map[string]Role{}
	for _, role := range Roles {
		for _, address := range envList(vars[role]) {
			out[address] = role
		}
		if len(envList(vars[role])) == 0 {
			out[defaults[role]] = role
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no account may sign in; set ACCOUNT_BOARD at least")
	}
	return out, nil
}

// readPaymentRoles reads which roles may record a payment.
func readPaymentRoles() (map[Role]bool, error) {
	names := envList("PAYMENT_ROLES")
	if len(names) == 0 {
		names = []string{string(RoleCashier)}
	}
	out := map[Role]bool{}
	for _, name := range names {
		role := Role(name)
		if !role.LoggedIn() {
			return nil, fmt.Errorf("PAYMENT_ROLES: %q is not a role (use intake, cashier or board)", name)
		}
		out[role] = true
	}
	return out, nil
}

// readServiceAccount loads the key from GOOGLE_SERVICE_ACCOUNT_FILE or, for a
// container that would rather pass a secret as an environment variable, from
// GOOGLE_SERVICE_ACCOUNT_JSON — plain or base64-encoded, because a JSON blob
// with newlines in it survives some deployment pipelines and not others.
//
// A missing key is not an error: the registry runs perfectly well without one
// and simply says on every page that nothing is being synchronised.
func readServiceAccount() (*ServiceAccount, error) {
	raw := []byte(strings.TrimSpace(os.Getenv("GOOGLE_SERVICE_ACCOUNT_JSON")))
	if path := strings.TrimSpace(os.Getenv("GOOGLE_SERVICE_ACCOUNT_FILE")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read GOOGLE_SERVICE_ACCOUNT_FILE: %w", err)
		}
		raw = b
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("the service account key is neither JSON nor base64: %w", err)
		}
		raw = decoded
	}
	var sa ServiceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("parse the service account key: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("the service account key has no client_email or private_key")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return &sa, nil
}

// demoSecret is fixed, so restarting a demo does not sign you out. It is
// public knowledge on purpose: demo mode has nothing worth protecting.
func demoSecret() []byte {
	sum := sha256.Sum256([]byte("rudbeckia-members-demo"))
	return sum[:]
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// envList reads a comma-separated, lowercased list, ignoring blanks.
func envList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			out = append(out, part)
		}
	}
	return out
}
