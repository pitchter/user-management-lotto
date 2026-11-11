// Package config reads the application's settings from environment variables
// and validates them once, at start-up. If any value is unsafe or makes no
// sense the process refuses to come up at all (fail fast), which is far better
// than discovering the problem once real traffic arrives.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Security policy constants.
const (
	// MinSecretLen is the shortest JWT secret accepted.
	//
	// RFC 7518 section 3.2 requires an HMAC key at least as long as the hash
	// function's output, which for HS256 (HMAC-SHA-256) is 32 bytes. A shorter
	// key weakens the signature and brings a brute-force search within reach.
	MinSecretLen = 32
	// MinBcryptCost and MaxBcryptCost bound the accepted bcrypt cost.
	MinBcryptCost = 10
	MaxBcryptCost = 15
)

// Config holds every setting of the application.
type Config struct {
	// AppEnv: development | production
	AppEnv string `env:"APP_ENV" envDefault:"development"`

	HTTP      HTTPConfig
	GRPC      GRPCConfig
	Mongo     MongoConfig
	JWT       JWTConfig
	Security  SecurityConfig
	Worker    WorkerConfig
	Log       LogConfig
	Bootstrap BootstrapConfig

	// ShutdownTimeout is how long in-flight requests are given to finish when
	// the process is asked to stop.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
}

// HTTPConfig configures the REST server.
type HTTPConfig struct {
	Port int `env:"HTTP_PORT" envDefault:"8080"`
	// ReadHeaderTimeout defends against Slowloris attacks, where a client
	// trickles headers in to hold connections open.
	ReadHeaderTimeout time.Duration `env:"HTTP_READ_HEADER_TIMEOUT" envDefault:"5s"`
	ReadTimeout       time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"15s"`
	WriteTimeout      time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"15s"`
	IdleTimeout       time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"60s"`
	// MaxBodyBytes caps the request body size to prevent memory exhaustion.
	MaxBodyBytes int64 `env:"HTTP_MAX_BODY_BYTES" envDefault:"1048576"`
}

// Addr returns the address to listen on.
func (c HTTPConfig) Addr() string { return fmt.Sprintf(":%d", c.Port) }

// GRPCConfig configures the gRPC server.
type GRPCConfig struct {
	Port    int  `env:"GRPC_PORT" envDefault:"50051"`
	Enabled bool `env:"GRPC_ENABLED" envDefault:"true"`
}

// Addr returns the address to listen on.
func (c GRPCConfig) Addr() string { return fmt.Sprintf(":%d", c.Port) }

// MongoConfig holds the MongoDB connection settings.
type MongoConfig struct {
	URI      string `env:"MONGO_URI" envDefault:"mongodb://localhost:27017"`
	Database string `env:"MONGO_DATABASE" envDefault:"user_management"`
	// Timeout applies to connecting, pinging and each individual operation.
	Timeout time.Duration `env:"MONGO_TIMEOUT" envDefault:"10s"`
}

// JWTConfig configures access tokens.
type JWTConfig struct {
	Secret    string        `env:"JWT_SECRET"`
	Issuer    string        `env:"JWT_ISSUER" envDefault:"user-management-api"`
	Audience  string        `env:"JWT_AUDIENCE" envDefault:"user-management-client"`
	AccessTTL time.Duration `env:"JWT_ACCESS_TTL" envDefault:"15m"`
	// Leeway absorbs small clock differences between machines when checking
	// exp and iat.
	Leeway time.Duration `env:"JWT_LEEWAY" envDefault:"30s"`
}

// SecurityConfig configures password hashing.
type SecurityConfig struct {
	BcryptCost int `env:"BCRYPT_COST" envDefault:"12"`
}

// WorkerConfig configures the background job.
type WorkerConfig struct {
	// UserCountInterval is how often the job logs the total number of users
	// (the challenge asks for every 10 seconds).
	UserCountInterval time.Duration `env:"USER_COUNT_INTERVAL" envDefault:"10s"`
	Enabled           bool          `env:"USER_COUNT_ENABLED" envDefault:"true"`
}

// LogConfig configures the logger.
type LogConfig struct {
	Level  string `env:"LOG_LEVEL" envDefault:"info"`
	Format string `env:"LOG_FORMAT" envDefault:"json"`
}

// BootstrapConfig is used to create the very first admin at start-up.
//
// The system uses RBAC where only an admin can create another admin, so
// without this path an empty database could never get its first one — the
// classic chicken-and-egg problem.
type BootstrapConfig struct {
	Name     string `env:"BOOTSTRAP_ADMIN_NAME" envDefault:"Admin"`
	Email    string `env:"BOOTSTRAP_ADMIN_EMAIL"`
	Password string `env:"BOOTSTRAP_ADMIN_PASSWORD"`
}

// Enabled reports whether a bootstrap admin should be created.
func (c BootstrapConfig) Enabled() bool {
	return c.Email != "" && c.Password != ""
}

// IsProduction reports whether the process runs in production mode.
//
// It gates the things that should not be on in production: gin's debug mode
// and gRPC server reflection.
func (c Config) IsProduction() bool {
	return strings.EqualFold(c.AppEnv, "production")
}

// Load reads the configuration from the environment.
//
// A .env file, if present, is loaded first as a convenience during local
// development, but real environment variables always win over the file,
// which matches how a container passes settings in.
func Load() (Config, error) {
	// godotenv.Load never overwrites a variable that is already set, and a
	// missing file is perfectly normal (containers do not ship one).
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(); err != nil {
			return Config{}, fmt.Errorf("could not read the .env file: %w", err)
		}
	}

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("could not read the environment variables: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks every value and collects all the problems into a single
// error, so whoever is configuring the service can fix them in one pass.
func (c Config) Validate() error {
	var problems []string

	if c.AppEnv != "development" && c.AppEnv != "production" {
		problems = append(problems, `APP_ENV must be either "development" or "production"`)
	}

	if len(c.JWT.Secret) == 0 {
		problems = append(problems, "JWT_SECRET must be set (generate one with: openssl rand -base64 48)")
	} else if len(c.JWT.Secret) < MinSecretLen {
		problems = append(problems, fmt.Sprintf(
			"JWT_SECRET must be at least %d bytes long (currently %d); a short secret is guessable and weakens HS256",
			MinSecretLen, len(c.JWT.Secret)))
	}
	if c.JWT.AccessTTL <= 0 {
		problems = append(problems, "JWT_ACCESS_TTL must be greater than 0")
	}
	if c.JWT.Issuer == "" {
		problems = append(problems, "JWT_ISSUER must not be empty")
	}
	if c.JWT.Audience == "" {
		problems = append(problems, "JWT_AUDIENCE must not be empty")
	}

	if c.Security.BcryptCost < MinBcryptCost || c.Security.BcryptCost > MaxBcryptCost {
		problems = append(problems, fmt.Sprintf(
			"BCRYPT_COST must be between %d and %d (currently %d)",
			MinBcryptCost, MaxBcryptCost, c.Security.BcryptCost))
	}

	if c.Mongo.URI == "" {
		problems = append(problems, "MONGO_URI must not be empty")
	} else if !strings.HasPrefix(c.Mongo.URI, "mongodb://") &&
		!strings.HasPrefix(c.Mongo.URI, "mongodb+srv://") {
		problems = append(problems, "MONGO_URI must start with mongodb:// or mongodb+srv://")
	}
	if c.Mongo.Database == "" {
		problems = append(problems, "MONGO_DATABASE must not be empty")
	}
	if c.Mongo.Timeout <= 0 {
		problems = append(problems, "MONGO_TIMEOUT must be greater than 0")
	}

	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		problems = append(problems, "HTTP_PORT must be between 1 and 65535")
	}
	if c.GRPC.Enabled && (c.GRPC.Port < 1 || c.GRPC.Port > 65535) {
		problems = append(problems, "GRPC_PORT must be between 1 and 65535")
	}
	if c.GRPC.Enabled && c.GRPC.Port == c.HTTP.Port {
		problems = append(problems, "GRPC_PORT must differ from HTTP_PORT")
	}
	if c.HTTP.MaxBodyBytes <= 0 {
		problems = append(problems, "HTTP_MAX_BODY_BYTES must be greater than 0")
	}

	if c.Worker.Enabled && c.Worker.UserCountInterval <= 0 {
		problems = append(problems, "USER_COUNT_INTERVAL must be greater than 0")
	}
	if c.ShutdownTimeout <= 0 {
		problems = append(problems, "SHUTDOWN_TIMEOUT must be greater than 0")
	}

	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, "LOG_LEVEL must be one of debug, info, warn, error")
	}
	switch strings.ToLower(c.Log.Format) {
	case "json", "text":
	default:
		problems = append(problems, "LOG_FORMAT must be either json or text")
	}

	// Setting the email but forgetting the password (or the other way round)
	// is usually a mistake, so say so instead of silently skipping the admin.
	if (c.Bootstrap.Email == "") != (c.Bootstrap.Password == "") {
		problems = append(problems,
			"BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD must be set together (or both left empty)")
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// Redacted returns the configuration in a form safe to log: no secrets, no passwords.
func (c Config) Redacted() map[string]any {
	return map[string]any{
		"app_env":             c.AppEnv,
		"http_port":           c.HTTP.Port,
		"grpc_port":           c.GRPC.Port,
		"grpc_enabled":        c.GRPC.Enabled,
		"mongo_database":      c.Mongo.Database,
		"jwt_issuer":          c.JWT.Issuer,
		"jwt_audience":        c.JWT.Audience,
		"jwt_access_ttl":      c.JWT.AccessTTL.String(),
		"bcrypt_cost":         c.Security.BcryptCost,
		"user_count_interval": c.Worker.UserCountInterval.String(),
		"log_level":           c.Log.Level,
		"log_format":          c.Log.Format,
		"shutdown_timeout":    c.ShutdownTimeout.String(),
		"bootstrap_admin":     c.Bootstrap.Enabled(),
	}
}
