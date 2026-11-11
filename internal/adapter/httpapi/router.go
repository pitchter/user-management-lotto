package httpapi

import (
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

	"github.com/pitchter/7-solution/internal/domain"
	"github.com/pitchter/7-solution/internal/platform/logger"
)

// RouterConfig is what the router needs to assemble every route.
type RouterConfig struct {
	Users  UserService
	Tokens domain.TokenService
	// Pinger backs /readyz (optional).
	Pinger Pinger
	Logger *slog.Logger
	// MaxBodyBytes caps the request body size (0 = unlimited).
	MaxBodyBytes int64
	// Production turns gin's debug mode off and hides anything not meant to
	// be exposed.
	Production bool
	Version    string
}

// APIPrefix is the prefix of every versioned endpoint.
//
// The version is there from the start so an /api/v2 can be added alongside
// later without breaking existing clients.
const APIPrefix = "/api/v1"

// validatorOnce stops RegisterTagNameFunc from being called again when more
// than one router is built (which happens in the test suite), because gin's
// validator is a single process-wide instance.
var validatorOnce sync.Once

// NewRouter assembles the gin engine with its middleware and routes.
//
// The middleware order matters: Recovery is outermost so it catches panics
// from every layer, then RequestID (so every log line has a correlation id),
// then Logging (which times the whole handler chain). Authenticate and
// RequireAdmin are attached only to the route groups that need protecting.
func NewRouter(cfg RouterConfig) *gin.Engine {
	if cfg.Logger == nil {
		cfg.Logger = logger.Discard()
	}
	if cfg.Production {
		gin.SetMode(gin.ReleaseMode)
	}

	useJSONFieldNames()

	r := gin.New()
	// Do not trust X-Forwarded-For from just anyone: a forged value would
	// attribute rate limits or log lines to the wrong caller. Behind a reverse
	// proxy, configure the real trusted proxies here.
	_ = r.SetTrustedProxies(nil)
	r.HandleMethodNotAllowed = true

	r.Use(Recovery(cfg.Logger), RequestID(cfg.Logger), Logging(cfg.Logger))
	if cfg.MaxBodyBytes > 0 {
		r.Use(MaxBodySize(cfg.MaxBodyBytes))
	}

	h := NewHandler(cfg.Users, cfg.Pinger, cfg.Version)

	// Answer 404 and 405 in the same error shape as everything else, rather
	// than gin's HTML.
	r.NoRoute(func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusNotFound, errorResponse{
			Error:     errorBody{Code: CodeNotFound, Message: "no such endpoint"},
			RequestID: RequestIDFrom(c),
		})
	})
	r.NoMethod(func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusMethodNotAllowed, errorResponse{
			Error:     errorBody{Code: CodeMethodNotAllowed, Message: "that method is not allowed on this endpoint"},
			RequestID: RequestIDFrom(c),
		})
	})

	// The probes need no authentication so the orchestrator can call them.
	r.GET("/healthz", h.Healthz)
	r.GET("/readyz", h.Readyz)

	v1 := r.Group(APIPrefix)
	{
		auth := v1.Group("/auth")
		{
			auth.POST("/register", h.Register)
			auth.POST("/login", h.Login)
		}

		users := v1.Group("/users")
		users.Use(Authenticate(cfg.Tokens))
		{
			// /users/me must come before /users/:id so "me" is not read as an id.
			users.GET("/me", h.GetMe)

			users.GET("", RequireAdmin(), h.ListUsers)
			users.POST("", RequireAdmin(), h.CreateUser)
			users.DELETE("/:id", RequireAdmin(), h.DeleteUser)

			// The user itself or an admin; the application layer checks ownership.
			users.GET("/:id", h.GetUser)
			users.PATCH("/:id", h.UpdateUser)
		}
	}

	return r
}

// useJSONFieldNames makes validator errors name fields by their json tag.
//
// By default validator reports the Go field name ("Email"), which does not
// match what the client sent ("email"), so the client cannot act on it.
func useJSONFieldNames() {
	validatorOnce.Do(func() {
		v, ok := binding.Validator.Engine().(*validator.Validate)
		if !ok {
			return
		}
		v.RegisterTagNameFunc(func(field reflect.StructField) string {
			name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
			if name == "" {
				name = strings.SplitN(field.Tag.Get("form"), ",", 2)[0]
			}
			if name == "-" {
				return ""
			}
			return name
		})
	})
}
