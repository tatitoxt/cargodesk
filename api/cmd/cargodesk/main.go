// Command cargodesk runs the API server and its maintenance tasks.
//
//	cargodesk serve          start the HTTP API (migrates first)
//	cargodesk migrate        apply pending migrations
//	cargodesk seed [-reset]  load synthetic demo data (-reset drops all data first)
//	cargodesk scan PATH...   scan files for PII/secret-shaped values
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/httpapi"
	"github.com/tatitoxt/cargodesk/api/internal/notify"
	"github.com/tatitoxt/cargodesk/api/internal/piiscan"
	"github.com/tatitoxt/cargodesk/api/internal/seed"
	"github.com/tatitoxt/cargodesk/api/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cargodesk serve|migrate|seed [-reset]|scan PATH...")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx, log)
	case "migrate":
		err = withDB(ctx, func(st *store.Store) error {
			applied, err := store.Migrate(ctx, st.Pool())
			log.Info("migrations applied", "applied", applied)
			return err
		})
	case "seed":
		err = runSeed(ctx, log, os.Args[2:])
	case "scan":
		err = scan(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
}

// attachmentsDir honours an explicitly empty ATTACHMENTS_DIR (uploads off).
func attachmentsDir() string {
	if v, ok := os.LookupEnv("ATTACHMENTS_DIR"); ok {
		return v
	}
	return "data/attachments"
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func withDB(ctx context.Context, fn func(*store.Store) error) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	return fn(store.New(pool))
}

func serve(ctx context.Context, log *slog.Logger) error {
	return withDB(ctx, func(st *store.Store) error {
		if _, err := store.Migrate(ctx, st.Pool()); err != nil {
			return err
		}
		demo := env("DEMO_AUTH", "false") == "true"
		var maxAtt int64 = 10 << 20
		if v := os.Getenv("MAX_ATTACHMENT_BYTES"); v != "" {
			if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
				maxAtt = n
			}
		}
		var oidc *httpapi.OIDCVerifier
		if iss, cid := os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"); iss != "" && cid != "" {
			oidc = httpapi.NewOIDCVerifier(iss, cid)
		}
		var mailer notify.Mailer
		if host := os.Getenv("SMTP_HOST"); host != "" {
			port, _ := strconv.Atoi(env("SMTP_PORT", "25"))
			mailer = &notify.SMTPMailer{
				Host: host, Port: port,
				User: os.Getenv("SMTP_USER"), Pass: os.Getenv("SMTP_PASS"),
				From: env("SMTP_FROM", "cargodesk@localhost"),
				TLS:  env("SMTP_TLS", "false") == "true",
			}
		}
		api := httpapi.New(httpapi.Config{Store: st, Logger: log, DemoAuth: demo, SecureCookie: env("SECURE_COOKIE", "false") == "true",
			AttachmentsDir: attachmentsDir(), MaxAttachmentBytes: maxAtt, OIDCVerifier: oidc,
			EmailEnabled: mailer != nil, SignupEnabled: env("SIGNUP_ENABLED", "false") == "true",
			TrustProxy:             env("TRUST_PROXY", "false") == "true",
			AllowCustomWhatsAppAPI: env("WHATSAPP_ALLOW_CUSTOM_API", "false") == "true"})
		srv := &http.Server{
			Addr:              env("ADDR", ":8080"),
			Handler:           api,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
		}
		errc := make(chan error, 1)
		go func() { errc <- srv.ListenAndServe() }()
		go (&notify.Dispatcher{Store: st, Log: log, Mailer: mailer}).Run(ctx)
		go retentionLoop(ctx, api, log)
		log.Info("listening", "addr", srv.Addr, "demo_auth", demo)
		select {
		case err := <-errc:
			return err
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return srv.Shutdown(shutdown)
		}
	})
}

func runSeed(ctx context.Context, log *slog.Logger, args []string) error {
	fset := flag.NewFlagSet("seed", flag.ContinueOnError)
	reset := fset.Bool("reset", false, "drop all data before seeding")
	if err := fset.Parse(args); err != nil {
		return err
	}
	return withDB(ctx, func(st *store.Store) error {
		if *reset {
			if err := store.ResetSchema(ctx, st.Pool()); err != nil {
				return err
			}
		}
		if _, err := store.Migrate(ctx, st.Pool()); err != nil {
			return err
		}
		var n int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM organizations`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			// seed.Run commits organizations before users, so an interrupted
			// seed leaves organizations without users — a marker on
			// organizations alone would then report "seeded" forever.
			var u int
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&u); err != nil {
				return err
			}
			if u == 0 {
				return fmt.Errorf("database is partially seeded (%d organizations, no users); rerun with -reset", n)
			}
			log.Info("database already seeded; use -reset to reload")
			return nil
		}
		if err := seed.Run(ctx, st.Pool(), time.Now()); err != nil {
			return err
		}
		log.Info("synthetic demo data loaded")
		return nil
	})
}

func scan(paths []string) error {
	var findings []piiscan.Finding
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			findings = append(findings, piiscan.Scan(p, b)...)
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, f := range findings {
		fmt.Printf("%s:%d: %s %s\n", f.File, f.Line, f.Rule, f.Match)
	}
	if len(findings) > 0 {
		return fmt.Errorf("%d finding(s)", len(findings))
	}
	fmt.Println("no PII/secret-shaped values found")
	return nil
}

// retentionLoop purges expired attachments on a timer. The interval defaults
// to hourly; RETENTION_INTERVAL overrides it ("0" disables the loop).
func retentionLoop(ctx context.Context, api *httpapi.Server, log *slog.Logger) {
	interval := time.Hour
	if v := os.Getenv("RETENTION_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Error("invalid RETENTION_INTERVAL", "value", v, "err", err)
			return
		}
		interval = d
	}
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := api.PurgeExpiredAttachments(ctx)
			if err != nil {
				log.Error("attachment retention purge failed", "err", err)
			} else if n > 0 {
				log.Info("attachment retention purge", "purged", n)
			}
		}
	}
}
