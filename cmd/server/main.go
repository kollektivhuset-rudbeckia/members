// Command server runs the Rudbeckia member register.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	// Embedding the timezone database lets the container run FROM scratch.
	_ "time/tzdata"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/demo"
	"github.com/kollektivhuset-rudbeckia/members/internal/google"
	"github.com/kollektivhuset-rudbeckia/members/internal/importer"
	"github.com/kollektivhuset-rudbeckia/members/internal/mattermost"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
	"github.com/kollektivhuset-rudbeckia/members/internal/web"
)

// version is stamped at build time with -ldflags.
var version = "dev"

func main() {
	var (
		checkConfig = flag.Bool("check-config", false,
			"load and validate the configuration file, then exit")
		showVersion = flag.Bool("version", false, "print the version and exit")
		demoMode    = flag.Bool("demo", false,
			"run a throwaway demo: invented members, no Google, a banner on every page")
		doImport = flag.Bool("import", false,
			"fill an empty register from the contact labels in the board's Google Contacts, then exit")
		dryRun = flag.Bool("dry-run", false,
			"with -import: report what would be imported without writing anything")
		importFrom = flag.String("import-from", "",
			"with -import: the mailbox to read (default: the board's account)")
		importJoined = flag.String("import-joined", "",
			"with -import: the day to record every imported member as having joined (YYYY-MM-DD)")
		importNote = flag.String("import-note", "Importerad från Google Kontakter",
			"with -import: the note written on every imported member")
		importBoard = flag.String("import-board", "",
			"read a Focalboard candidate export and fill the pipeline from it, then exit")
		clearBoNotes = flag.Bool("clear-resident-notes", false,
			"empty the note on every current bomedlem, then exit; use with -dry-run first")
	)
	flag.Parse()

	if *demoMode {
		os.Setenv("DEMO", "true")
	}
	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(os.Getenv("LOG_LEVEL")),
	}))
	slog.SetDefault(log)

	if *checkConfig {
		if err := check(log); err != nil {
			log.Error("the configuration is not valid", "err", err)
			os.Exit(1)
		}
		return
	}

	if *importBoard != "" {
		if err := runBoardImport(log, *importBoard, *dryRun); err != nil {
			log.Error("the board import failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if *clearBoNotes {
		if err := runClearResidentNotes(log, *dryRun); err != nil {
			log.Error("could not clear the notes", "err", err)
			os.Exit(1)
		}
		return
	}

	if *doImport {
		opts := importer.Options{
			Mailbox: *importFrom, Note: *importNote,
			DryRun: *dryRun, AlsoCheckGroups: true,
		}
		if err := runImport(log, opts, *importJoined); err != nil {
			log.Error("the import failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// check validates the configuration without needing any credentials, so it
// can run in CI and as a pre-flight step before a deploy.
func check(log *slog.Logger) error {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	log.Info("the configuration is valid",
		"path", path,
		"timezone", cfg.Site.Timezone,
		"language", cfg.Site.Language,
		"fee", cfg.Membership.FeeKr,
		"due", cfg.Membership.DueOn,
		"targets", cfg.Targets(),
		"every", cfg.Sync.Interval())
	for _, g := range cfg.Groups {
		log.Info("group", "kind", g.Kind, "email", g.Email,
			"prunes", g.Pruning(), "kept", len(g.Keep))
	}
	for _, kind := range config.Kinds {
		log.Info("contact label", "kind", kind, "label", cfg.Contacts.LabelFor(kind))
	}
	return nil
}

// setup does everything the server and the importer both need: read the
// configuration, open the database, and build the Google client if there is a
// key for one.
func setup(log *slog.Logger) (*config.Config, config.Runtime, *store.Store, *google.Client, error) {
	rt, err := config.LoadRuntime()
	if err != nil {
		return nil, rt, nil, nil, err
	}
	cfg, err := config.Load(rt.ConfigPath)
	if err != nil {
		return nil, rt, nil, nil, err
	}
	// The Workspace's own domain folds dots like any other Google domain, and
	// it is only known from the environment.
	cfg.WithHostedDomain(rt.Google.HostedDomain)

	st, err := store.Open(rt.DBPath)
	if err != nil {
		return nil, rt, nil, nil, err
	}

	var gc *google.Client
	if rt.Google.SyncReady() {
		if gc, err = google.New(rt.Google.ServiceAccount); err != nil {
			st.Close()
			return nil, rt, nil, nil, fmt.Errorf("service account: %w", err)
		}
	}
	return cfg, rt, st, gc, nil
}

// runImport is the migration: read what the association already keeps in
// Google Contacts and write it into an empty register.
func runImport(log *slog.Logger, opts importer.Options, joined string) error {
	cfg, rt, st, gc, err := setup(log)
	if err != nil {
		return err
	}
	defer st.Close()

	if gc == nil {
		return errors.New("an import reads Google Contacts, so it needs a service account: " +
			"set GOOGLE_SERVICE_ACCOUNT_FILE and GOOGLE_ADMIN_SUBJECT")
	}
	if opts.Mailbox == "" {
		opts.Mailbox = rt.AccountFor(config.RoleBoard)
	}
	if opts.Mailbox == "" {
		return errors.New("no mailbox to import from: set -import-from")
	}
	if opts.Actor == "" {
		opts.Actor = opts.Mailbox
	}
	if joined != "" {
		day, err := store.ParseDay(joined, cfg.Location())
		if err != nil {
			return fmt.Errorf("-import-joined %q is not a date: %w", joined, err)
		}
		opts.JoinedOn = day
	} else if !opts.DryRun {
		return errors.New("-import-joined is required: a contact card does not say when " +
			"somebody joined, and guessing would put a wrong number in the one column " +
			"the register exists to provide. Use the association's founding date, or today's")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := importer.Run(ctx, gc, st, cfg, rt, opts)
	if err != nil {
		return err
	}

	fmt.Printf("\nLäste %s i %s\n", opts.Mailbox, "Google Kontakter")
	report("Kan importeras", result.Ready())
	report("Finns redan i registret", result.Skipped())
	report("Behöver rättas för hand", result.Problems())

	if len(result.StrayInGroups) > 0 {
		fmt.Printf("\n  I en Google-grupp men under ingen etikett — %d st.\n", len(result.StrayInGroups))
		fmt.Println("  De här försvinner ur gruppen vid första synken om de inte")
		fmt.Println("  läggs in i registret eller i groups[].keep i config.yaml:")
		for _, address := range result.StrayInGroups {
			fmt.Println("    " + address)
		}
	}

	if result.DryRun {
		fmt.Printf("\nIngenting skrevs. Kör om utan -dry-run och med -import-joined för att importera %d medlemmar.\n",
			len(result.Ready()))
		return nil
	}
	fmt.Printf("\nImporterade %d medlemmar.\n", result.Imported)
	return nil
}

// runBoardImport fills the candidate pipeline from the Focalboard export.
//
// It needs no Google at all: the board is a plain file and the pipeline is
// the register's own. That makes it the one import somebody can rehearse on a
// laptop before it touches the server.
func runBoardImport(log *slog.Logger, path string, dryRun bool) error {
	rt, err := config.LoadRuntime()
	if err != nil {
		return err
	}
	cfg, err := config.Load(rt.ConfigPath)
	if err != nil {
		return err
	}
	cfg.WithHostedDomain(rt.Google.HostedDomain)

	st, err := store.Open(rt.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open the export: %w", err)
	}
	defer f.Close()

	actor := rt.AccountFor(config.RoleIntake)
	result, err := importer.ImportBoard(context.Background(), st, cfg, f, dryRun, actor)
	if err != nil {
		return err
	}

	fmt.Printf("\nLäste %s\n", path)
	fmt.Printf("\n  Rubrikerna i filen blev de här stegen:\n")
	headings := make([]string, 0, len(result.Stages))
	for h := range result.Stages {
		headings = append(headings, h)
	}
	sort.Strings(headings)
	for _, h := range headings {
		fmt.Printf("    %-36s → %s\n", h, result.Stages[h])
	}

	reportRows("Kan läggas till", result.Ready())
	reportRows("Finns redan på tavlan", result.Skipped())
	reportRows("Hoppas över, redan ur processen", result.Settled())
	reportRows("Värt en titt, men läggs till", result.Warnings())
	reportRows("Kan inte läggas till", result.Problems())

	if result.DryRun {
		fmt.Printf("\nIngenting skrevs. Kör om utan -dry-run för att lägga till %d kandidater.\n",
			len(result.Ready()))
		return nil
	}
	fmt.Printf("\nLade till %d kandidater.\n", result.Imported)
	return nil
}

func reportRows(heading string, list []importer.CandidateRow) {
	fmt.Printf("\n  %s — %d st.\n", heading, len(list))
	for _, c := range list {
		name := c.Name
		if name == "" {
			name = "(utan namn)"
		}
		line := fmt.Sprintf("    %-30s %-34s %-10s %s", name, c.Email, c.Stage, c.Apartment)
		switch {
		case c.Problem != "":
			line += "  ← " + c.Problem
		case c.Warning != "":
			line += "  ← " + c.Warning
		}
		fmt.Println(strings.TrimRight(line, " "))
	}
}

func report(heading string, list []importer.Candidate) {
	fmt.Printf("\n  %s — %d st.\n", heading, len(list))
	for _, c := range list {
		line := fmt.Sprintf("    %-28s %-34s %s", c.Name(), c.Email, c.Kind)
		if c.Problem != "" {
			line += "  ← " + c.Problem
		}
		fmt.Println(line)
	}
}

func run(log *slog.Logger) error {
	cfg, rt, st, gc, err := setup(log)
	if err != nil {
		return err
	}
	defer st.Close()

	log.Info("configuration loaded",
		"path", rt.ConfigPath, "timezone", cfg.Site.Timezone,
		"language", cfg.Site.Language, "targets", cfg.Targets())

	if rt.Demo {
		n, err := demo.Seed(context.Background(), st, cfg, time.Now())
		if err != nil {
			return fmt.Errorf("seed the demo: %w", err)
		}
		log.Warn("DEMO MODE — anybody may sign in as any role, and nothing reaches Google",
			"seeded_members", n, "database", rt.DBPath)
	} else {
		for _, role := range config.Roles {
			log.Info("account", "role", role, "email", rt.AccountFor(role),
				"may_record_payments", rt.Access.May(role, config.PermPay))
		}
	}

	syncer := sync.New(cfg, rt, st, gc, log)

	ctx, stopSync := context.WithCancel(context.Background())
	defer stopSync()

	if syncer.Enabled() {
		// Check every target before the first pass, so a typo in config.yaml
		// or a scope nobody granted is a line in the log within seconds
		// rather than a mystery ten minutes later. It never stops the server:
		// a register that will not start because Google is having a bad
		// morning is worse than one that starts and says so on every page.
		verifyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		problems := syncer.Verify(verifyCtx)
		cancel()
		for _, p := range problems {
			log.Error("a synchronisation target is not reachable", "problem", p)
		}
		if len(problems) == 0 {
			log.Info("every synchronisation target answered", "targets", cfg.Targets())
		}
		go syncer.Run(ctx)
	} else if !rt.Demo {
		log.Warn("no Google service account is configured: the register works, " +
			"but the groups, the contacts and the spreadsheet are not being kept in step")
	}

	// The chat bot. Check the token now: a bot that cannot log in must be a
	// startup complaint rather than a mystery the first time somebody
	// registers an interest through the public page.
	chat := mattermost.New(rt.Chat.URL, rt.Chat.Token, log)
	if chat.Enabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		me, err := chat.Verify(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("mattermost: %w", err)
		}
		switch {
		case rt.Chat.TestUser != "":
			log.Warn("mattermost is in test mode: every announcement goes to one "+
				"person as a direct message and no channel is written to",
				"bot", me.Username, "to", rt.Chat.TestUser)
		default:
			log.Info("mattermost bot ready", "server", rt.Chat.URL, "bot", me.Username,
				"candidates_channel", cfg.Chat.Candidates, "registry_channel", cfg.Chat.Registry)
		}
	} else if !rt.Demo {
		log.Warn("Mattermost is not configured: changes are recorded and logged, " +
			"but nothing is announced in the chat")
	}

	guard := auth.New(rt)
	srv, err := web.New(cfg, rt, st, guard, syncer, chat, log)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              rt.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Long enough for a synchronisation run by hand, which talks to
		// Google several times before it can render the result.
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", rt.ListenAddr, "base_url", rt.BaseURL, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String())
	}

	stopSync()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// runClearResidentNotes is the one-off pass that brings the register up to the
// rule: a resident member carries no note from their time as a friend member.
//
// It exists because the rule arrived after the members did. Everybody who had
// already moved in kept the interview note they were welcomed with, and those
// notes are not only in the register — the note column goes into the
// spreadsheet the cashiers work in.
//
// Every change is written to the audit trail with the old text in it, one line
// per member, so nothing here is unrecoverable: the note can be read back off
// the log and typed in again if any of it turns out to have been wanted.
func runClearResidentNotes(log *slog.Logger, dryRun bool) error {
	// Deliberately not config.LoadRuntime(): this touches nothing but the
	// database on disk, and a local pass over local rows has no business
	// refusing to run because an OAuth client is not configured. It reads the
	// same two environment variables the server does, and nothing else.
	configPath := envOr("CONFIG_PATH", "config.yaml")
	dbPath := envOr("DB_PATH", "data/members.db")

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	members, err := st.Members(ctx, cfg.Location())
	if err != nil {
		return err
	}

	var hit []store.Member
	for _, m := range members {
		if m.Kind == config.KindBo && strings.TrimSpace(m.Note) != "" {
			hit = append(hit, m)
		}
	}

	if len(hit) == 0 {
		fmt.Println("Ingen bomedlem har någon anteckning. Ingenting att göra.")
		return nil
	}

	fmt.Printf("\n%d bomedlemmar har en anteckning:\n\n", len(hit))
	for _, m := range hit {
		fmt.Printf("  %-28s %-32s %s\n", m.Name(), m.Email, quoteNote(m.Note))
	}

	if dryRun {
		fmt.Printf("\n-dry-run: ingenting ändrades. Kör om utan -dry-run för att tömma dem.\n")
		return nil
	}

	actor := envOr("ACCOUNT_BOARD", "")
	if actor == "" {
		// The trail has to name somebody, and a command run by hand is not a
		// person. Saying so is better than borrowing an account's name.
		actor = "-clear-resident-notes"
	}
	now := time.Now().In(cfg.Location())

	var done int
	for _, m := range hit {
		before := m
		m.Note = ""
		m.UpdatedAt, m.UpdatedBy = now, actor
		if err := st.UpdateMember(ctx, m); err != nil {
			log.Error("could not clear a note", "member", before.Email, "err", err)
			continue
		}
		// The old text goes in the trail, which is what makes this reversible.
		err := st.Log(ctx, store.Entry{
			At: now, Actor: actor, Role: string(config.RoleBoard),
			Action: "member.changed", MemberID: before.ID,
			Subject: before.Name() + " <" + before.Email + ">",
			Detail:  "anteckning: " + quoteNote(before.Note) + " → \"\"; bomedlem behåller ingen anteckning från kandidattiden",
		})
		if err != nil {
			log.Error("could not write the audit trail", "member", before.Email, "err", err)
		}
		done++
	}

	fmt.Printf("\nTömde anteckningen på %d av %d bomedlemmar.\n", done, len(hit))
	fmt.Println("Gamla texter finns i loggen på skötselsidan om något behöver tillbaka.")
	if done > 0 {
		fmt.Println("Kalkylarkets anteckningskolumn uppdateras vid nästa synk.")
	}
	return nil
}

// quoteNote renders a note for the terminal and the trail, on one line.
func quoteNote(s string) string {
	return "\"" + strings.Join(strings.Fields(s), " ") + "\""
}

// envOr is os.Getenv with a fallback, for the commands that run without a
// full runtime configuration.
func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
