package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pitchter/7-solution/internal/platform/config"
)

// validConfig returns a configuration that passes every rule, so each test
// only has to change the field it cares about.
func validConfig() config.Config {
	return config.Config{
		AppEnv: "development",
		HTTP: config.HTTPConfig{
			Port:         8080,
			MaxBodyBytes: 1 << 20,
		},
		GRPC: config.GRPCConfig{Port: 50051, Enabled: true},
		Mongo: config.MongoConfig{
			URI:      "mongodb://localhost:27017",
			Database: "user_management",
			Timeout:  10 * time.Second,
		},
		JWT: config.JWTConfig{
			Secret:    strings.Repeat("s", config.MinSecretLen),
			Issuer:    "user-management-api",
			Audience:  "user-management-client",
			AccessTTL: 15 * time.Minute,
		},
		Security: config.SecurityConfig{BcryptCost: 12},
		Worker: config.WorkerConfig{
			UserCountInterval: 10 * time.Second,
			Enabled:           true,
		},
		Log:             config.LogConfig{Level: "info", Format: "json"},
		ShutdownTimeout: 15 * time.Second,
	}
}

func TestValidateAcceptsGoodConfig(t *testing.T) {
	t.Parallel()

	if err := validConfig().Validate(); err != nil {
		t.Fatalf("a valid configuration must pass, got: %v", err)
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(*config.Config)
		wantText string
	}{
		{
			name:     "no JWT secret",
			mutate:   func(c *config.Config) { c.JWT.Secret = "" },
			wantText: "JWT_SECRET",
		},
		{
			name:     "JWT secret too short",
			mutate:   func(c *config.Config) { c.JWT.Secret = "too-short" },
			wantText: "JWT_SECRET",
		},
		{
			name:     "one byte under the minimum still fails",
			mutate:   func(c *config.Config) { c.JWT.Secret = strings.Repeat("s", config.MinSecretLen-1) },
			wantText: "JWT_SECRET",
		},
		{
			name:     "TTL of zero",
			mutate:   func(c *config.Config) { c.JWT.AccessTTL = 0 },
			wantText: "JWT_ACCESS_TTL",
		},
		{
			name:     "bcrypt cost too low",
			mutate:   func(c *config.Config) { c.Security.BcryptCost = 4 },
			wantText: "BCRYPT_COST",
		},
		{
			name:     "bcrypt cost too high",
			mutate:   func(c *config.Config) { c.Security.BcryptCost = 31 },
			wantText: "BCRYPT_COST",
		},
		{
			name:     "MONGO_URI with the wrong scheme",
			mutate:   func(c *config.Config) { c.Mongo.URI = "postgres://localhost" },
			wantText: "MONGO_URI",
		},
		{
			name:     "no database name",
			mutate:   func(c *config.Config) { c.Mongo.Database = "" },
			wantText: "MONGO_DATABASE",
		},
		{
			name:     "HTTP port out of range",
			mutate:   func(c *config.Config) { c.HTTP.Port = 70000 },
			wantText: "HTTP_PORT",
		},
		{
			name:     "gRPC port clashes with HTTP",
			mutate:   func(c *config.Config) { c.GRPC.Port = c.HTTP.Port },
			wantText: "GRPC_PORT",
		},
		{
			name:     "unknown APP_ENV",
			mutate:   func(c *config.Config) { c.AppEnv = "staging" },
			wantText: "APP_ENV",
		},
		{
			name:     "unknown LOG_LEVEL",
			mutate:   func(c *config.Config) { c.Log.Level = "verbose" },
			wantText: "LOG_LEVEL",
		},
		{
			name:     "unknown LOG_FORMAT",
			mutate:   func(c *config.Config) { c.Log.Format = "xml" },
			wantText: "LOG_FORMAT",
		},
		{
			name:     "worker interval of zero while enabled",
			mutate:   func(c *config.Config) { c.Worker.UserCountInterval = 0 },
			wantText: "USER_COUNT_INTERVAL",
		},
		{
			name:     "shutdown timeout of zero",
			mutate:   func(c *config.Config) { c.ShutdownTimeout = 0 },
			wantText: "SHUTDOWN_TIMEOUT",
		},
		{
			name:     "bootstrap email without a password",
			mutate:   func(c *config.Config) { c.Bootstrap.Email = "admin@example.com" },
			wantText: "BOOTSTRAP_ADMIN",
		},
		{
			name:     "bootstrap password without an email",
			mutate:   func(c *config.Config) { c.Bootstrap.Password = "supersecret" },
			wantText: "BOOTSTRAP_ADMIN",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("an invalid configuration must not pass")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("the error message must mention %q, got: %v", tc.wantText, err)
			}
		})
	}
}

func TestValidateReportsAllProblemsAtOnce(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.JWT.Secret = ""
	cfg.Mongo.Database = ""
	cfg.Security.BcryptCost = 1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"JWT_SECRET", "MONGO_DATABASE", "BCRYPT_COST"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q must be reported in the same pass, got: %v", want, err)
		}
	}
}

func TestGRPCPortNotValidatedWhenDisabled(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.GRPC.Enabled = false
	cfg.GRPC.Port = 0

	if err := cfg.Validate(); err != nil {
		t.Errorf("with gRPC disabled the port must not be checked, got: %v", err)
	}
}

func TestIsProduction(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"production":  true,
		"PRODUCTION":  true,
		"development": false,
		"":            false,
	}
	for env, want := range tests {
		cfg := validConfig()
		cfg.AppEnv = env
		if got := cfg.IsProduction(); got != want {
			t.Errorf("AppEnv=%q: IsProduction() = %v, want %v", env, got, want)
		}
	}
}

func TestBootstrapEnabled(t *testing.T) {
	t.Parallel()

	if (config.BootstrapConfig{}).Enabled() {
		t.Error("with nothing set it must not be enabled")
	}
	full := config.BootstrapConfig{Email: "a@example.com", Password: "supersecret"}
	if !full.Enabled() {
		t.Error("with both set it must be enabled")
	}
}

func TestAddrHelpers(t *testing.T) {
	t.Parallel()

	if got := (config.HTTPConfig{Port: 8080}).Addr(); got != ":8080" {
		t.Errorf("HTTP Addr = %q", got)
	}
	if got := (config.GRPCConfig{Port: 50051}).Addr(); got != ":50051" {
		t.Errorf("gRPC Addr = %q", got)
	}
}

func TestRedactedHidesSecrets(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.JWT.Secret = "super-secret-value-that-must-not-leak"
	cfg.Bootstrap = config.BootstrapConfig{Email: "a@example.com", Password: "p@ssw0rd-long"}

	redacted := cfg.Redacted()
	for k, v := range redacted {
		if s, ok := v.(string); ok {
			if strings.Contains(s, "super-secret-value") || strings.Contains(s, "p@ssw0rd") {
				t.Errorf("field %q still carries a secret: %v", k, v)
			}
		}
	}
	if _, found := redacted["jwt_secret"]; found {
		t.Error("Redacted must not contain a jwt_secret key at all")
	}
	if redacted["bootstrap_admin"] != true {
		t.Error("it must only say whether a bootstrap admin exists (true/false)")
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	// No t.Parallel(): t.Setenv mutates the process-wide environment.
	t.Setenv("JWT_SECRET", strings.Repeat("k", 40))
	t.Setenv("HTTP_PORT", "9999")
	t.Setenv("USER_COUNT_INTERVAL", "3s")
	t.Setenv("BCRYPT_COST", "11")
	t.Chdir(t.TempDir()) // so the project's own .env is not picked up

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load must succeed, got: %v", err)
	}
	if cfg.HTTP.Port != 9999 {
		t.Errorf("HTTP.Port = %d", cfg.HTTP.Port)
	}
	if cfg.Worker.UserCountInterval != 3*time.Second {
		t.Errorf("UserCountInterval = %v", cfg.Worker.UserCountInterval)
	}
	if cfg.Security.BcryptCost != 11 {
		t.Errorf("BcryptCost = %d", cfg.Security.BcryptCost)
	}
	// Anything not set must come from envDefault.
	if cfg.Mongo.Database != "user_management" {
		t.Errorf("Database default = %q", cfg.Mongo.Database)
	}
	if cfg.Worker.Enabled != true {
		t.Error("the worker must be enabled by default")
	}
}

func TestLoadFailsWithoutSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Chdir(t.TempDir())

	if _, err := config.Load(); err == nil {
		t.Fatal("without JWT_SECRET the application must refuse to start")
	}
}
