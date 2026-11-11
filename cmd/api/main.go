// Command api is the entry point of the User Management API.
//
// This file is the application's single composition root: it reads the
// configuration, builds every adapter, wires them into the use cases and
// starts the servers. No business rule lives here. Keeping the wiring in one
// place makes the whole system legible from a single file, and means no other
// part has to know who constructed it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"user-management-api/internal/adapter/crypto"
	"user-management-api/internal/adapter/grpcapi"
	"user-management-api/internal/adapter/httpapi"
	"user-management-api/internal/adapter/jwtauth"
	"user-management-api/internal/adapter/mongodb"
	"user-management-api/internal/app"
	"user-management-api/internal/platform/config"
	"user-management-api/internal/platform/logger"
	"user-management-api/internal/worker"
)

// Values stamped in at build time with -ldflags (see the Makefile).
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	// Self-check mode, used by Docker's HEALTHCHECK.
	//
	// The published image is distroless, so there is no shell, curl or wget to
	// call. Letting the same binary answer this keeps the image small and its
	// attack surface minimal while still giving a real health check.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := selfHealthCheck(); err != nil {
			fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		// The logger may not exist yet (bad configuration), so use stderr.
		fmt.Fprintf(os.Stderr, "could not start: %v\n", err)
		os.Exit(1)
	}
}

// selfHealthCheck sends GET /healthz to this process and expects a 200.
func selfHealthCheck() error {
	// Parsing the port to an integer first means the value coming from the
	// environment can only ever be a usable port, never another part of a URL.
	port := 8080
	if raw := os.Getenv("HTTP_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 65535 {
			return fmt.Errorf("HTTP_PORT is invalid: %q", raw)
		}
		port = parsed
	}

	// Loopback only; the destination never comes from external input.
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)

	client := &http.Client{Timeout: 3 * time.Second}

	// gosec treats a URL built with an environment value as an SSRF risk, but
	// here the host is pinned to 127.0.0.1 and the only environment-derived
	// part is the port, already parsed into the range 1-65535. The request can
	// therefore only ever reach this process.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil) //nolint:gosec // loopback only, with a validated port
	if err != nil {
		return err
	}

	res, err := client.Do(req) //nolint:gosec // destination is our own loopback (see above)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("got status %d", res.StatusCode)
	}
	return nil
}

func run() error {
	// This context is cancelled on SIGINT (Ctrl+C) or SIGTERM (docker stop,
	// kubectl delete), which is what starts the graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(os.Stdout, logger.Options{
		Level:  cfg.Log.Level,
		Format: cfg.Log.Format,
	})
	// Also set as the default so libraries calling slog land in the same place.
	slog.SetDefault(log)

	log.Info("starting",
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("build_date", buildDate),
		slog.Any("config", cfg.Redacted()),
	)

	// --- adapter: MongoDB ---
	mongoClient, err := mongodb.Connect(ctx, mongodb.Config{
		URI:      cfg.Mongo.URI,
		Database: cfg.Mongo.Database,
		Timeout:  cfg.Mongo.Timeout,
	})
	if err != nil {
		return err
	}
	defer closeMongo(mongoClient, cfg.ShutdownTimeout, log)

	// Build the indexes at start-up so unique email holds from the first request.
	indexCtx, cancelIndex := context.WithTimeout(ctx, cfg.Mongo.Timeout)
	defer cancelIndex()
	if indexErr := mongoClient.EnsureIndexes(indexCtx); indexErr != nil {
		return indexErr
	}
	log.Info("mongodb_ready", slog.String("database", cfg.Mongo.Database))

	// --- adapter: security ---
	hasher := crypto.NewBcryptHasher(cfg.Security.BcryptCost)
	tokens, err := jwtauth.NewTokenService(jwtauth.Config{
		Secret:   cfg.JWT.Secret,
		Issuer:   cfg.JWT.Issuer,
		Audience: cfg.JWT.Audience,
		TTL:      cfg.JWT.AccessTTL,
		Leeway:   cfg.JWT.Leeway,
	})
	if err != nil {
		return err
	}

	// --- use case ---
	repo := mongodb.NewUserRepository(mongoClient.Database())
	users, err := app.NewUserService(app.Deps{
		Repo:   repo,
		Hasher: hasher,
		Tokens: tokens,
		Logger: log,
	})
	if err != nil {
		return err
	}

	if err := bootstrapAdmin(ctx, cfg, users, log); err != nil {
		return err
	}

	// --- servers ---
	httpServer := newHTTPServer(cfg, users, tokens, mongoClient, log)

	// gRPC server, calling the very same use cases as REST.
	grpcServer := grpcapi.NewServer(grpcapi.Config{
		Users:  users,
		Tokens: tokens,
		Logger: log,
		// Reflection makes grpcurl usable during development but exposes the
		// whole schema, so it is off in production.
		EnableReflection: !cfg.IsProduction(),
		MaxRecvMsgBytes:  int(cfg.HTTP.MaxBodyBytes),
	})

	// The errgroup runs the HTTP server, the background worker and the
	// shutdown watcher together. If any of them fails, gctx is cancelled and
	// the others wind down after it.
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Info("http_listening", slog.String("addr", cfg.HTTP.Addr()))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("the HTTP server stopped: %w", err)
		}
		return nil
	})

	if cfg.GRPC.Enabled {
		var lc net.ListenConfig
		listener, listenErr := lc.Listen(ctx, "tcp", cfg.GRPC.Addr())
		if listenErr != nil {
			return fmt.Errorf("could not open the gRPC port: %w", listenErr)
		}

		g.Go(func() error {
			log.Info("grpc_listening",
				slog.String("addr", cfg.GRPC.Addr()),
				slog.Bool("reflection", !cfg.IsProduction()),
			)
			if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				return fmt.Errorf("the gRPC server stopped: %w", err)
			}
			return nil
		})
	}

	if cfg.Worker.Enabled {
		counter := worker.NewUserCounter(repo, worker.Options{
			Interval: cfg.Worker.UserCountInterval,
			Logger:   log,
		})
		g.Go(func() error { return counter.Run(gctx) })
	}

	// Wait for the stop signal, then shut the servers down gently.
	g.Go(func() error {
		<-gctx.Done()
		log.Info("shutdown_initiated", slog.String("timeout", cfg.ShutdownTimeout.String()))

		// Use a context detached from gctx (already cancelled); otherwise
		// Shutdown would return at once and cut off in-flight requests.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(gctx), cfg.ShutdownTimeout)
		defer cancel()

		// Stop gRPC gracefully in parallel with HTTP, so the combined wait
		// cannot exceed the timeout.
		grpcStopped := make(chan struct{})
		if cfg.GRPC.Enabled {
			go func() {
				grpcServer.GracefulStop()
				close(grpcStopped)
			}()
		} else {
			close(grpcStopped)
		}

		// Shutdown stops accepting connections and lets in-flight requests finish.
		httpErr := httpServer.Shutdown(shutdownCtx)
		if httpErr == nil {
			log.Info("http_stopped")
		}

		select {
		case <-grpcStopped:
			log.Info("grpc_stopped")
		case <-shutdownCtx.Done():
			// Out of time: force it down so the process cannot hang forever.
			log.Warn("grpc_force_stopped")
			grpcServer.Stop()
		}

		if httpErr != nil {
			return fmt.Errorf("the HTTP server did not shut down cleanly: %w", httpErr)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	log.Info("shutdown_complete")
	return nil
}

// newHTTPServer assembles the HTTP server with the timeouts it needs.
func newHTTPServer(
	cfg config.Config,
	users httpapi.UserService,
	tokens *jwtauth.TokenService,
	pinger httpapi.Pinger,
	log *slog.Logger,
) *http.Server {
	router := httpapi.NewRouter(httpapi.RouterConfig{
		Users:        users,
		Tokens:       tokens,
		Pinger:       pinger,
		Logger:       log,
		MaxBodyBytes: cfg.HTTP.MaxBodyBytes,
		Production:   cfg.IsProduction(),
		Version:      version,
	})

	return &http.Server{
		Addr:              cfg.HTTP.Addr(),
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// bootstrapAdmin creates the first admin when one is configured.
func bootstrapAdmin(ctx context.Context, cfg config.Config, users *app.UserService, log *slog.Logger) error {
	if !cfg.Bootstrap.Enabled() {
		return nil
	}

	// EnsureBootstrapAdmin logs bootstrap_admin_created itself when it creates
	// one, so only the other outcome is reported here.
	created, err := users.EnsureBootstrapAdmin(ctx,
		cfg.Bootstrap.Name, cfg.Bootstrap.Email, cfg.Bootstrap.Password)
	if err != nil {
		return fmt.Errorf("could not prepare the bootstrap admin: %w", err)
	}

	if !created {
		log.Info("bootstrap_admin_exists", slog.String("email", cfg.Bootstrap.Email))
	}
	return nil
}

// closeMongo closes the connection pool last, once the servers have stopped.
func closeMongo(c *mongodb.Client, timeout time.Duration, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := c.Close(ctx); err != nil {
		log.Error("mongodb_close_failed", slog.String(logger.KeyError, err.Error()))
		return
	}
	log.Info("mongodb_closed")
}
