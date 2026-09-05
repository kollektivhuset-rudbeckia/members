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

	guard := auth.New(rt)
	srv, err := web.New(cfg, rt, st, guard, syncer, log)
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
