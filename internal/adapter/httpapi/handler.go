package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"user-management-api/internal/app"
	"user-management-api/internal/domain"
)

// UserService is what the HTTP layer needs from the application layer.
//
// The interface is declared on the consumer side, not next to the provider,
// following Go's "accept interfaces, return structs". That lets handlers be
// tested with a stub and makes it obvious which parts of the service this
// adapter actually uses.
type UserService interface {
	Register(ctx context.Context, in app.RegisterInput) (app.UserOutput, error)
	Authenticate(ctx context.Context, in app.LoginInput) (app.AuthOutput, error)
	Create(ctx context.Context, actor domain.Identity, in app.CreateUserInput) (app.UserOutput, error)
	GetByID(ctx context.Context, actor domain.Identity, id string) (app.UserOutput, error)
	List(ctx context.Context, actor domain.Identity, in app.ListInput) (app.ListOutput, error)
	Update(ctx context.Context, actor domain.Identity, id string, in app.UpdateUserInput) (app.UserOutput, error)
	Delete(ctx context.Context, actor domain.Identity, id string) error
}

// Pinger checks that the database still answers (for the readiness probe).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler holds the dependencies every endpoint shares.
type Handler struct {
	users   UserService
	pinger  Pinger
	version string
}

// NewHandler builds the handler.
func NewHandler(users UserService, pinger Pinger, version string) *Handler {
	return &Handler{users: users, pinger: pinger, version: version}
}

// --- auth ---

// Register handles POST /api/v1/auth/register.
//
// Whoever signs up gets the user role, whatever the body claims.
func (h *Handler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBindError(c, err)
		return
	}

	out, err := h.users.Register(c.Request.Context(), app.RegisterInput{
		Name:     req.Name,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		respondError(c, err)
		return
	}

	c.Header("Location", "/api/v1/users/"+out.ID)
	c.JSON(http.StatusCreated, newUserResponse(out))
}

// Login handles POST /api/v1/auth/login.
func (h *Handler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBindError(c, err)
		return
	}

	out, err := h.users.Authenticate(c.Request.Context(), app.LoginInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		respondError(c, err)
		return
	}

	// A token must not be cached by a proxy or a browser.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, newTokenResponse(out))
}

// --- users ---

// CreateUser handles POST /api/v1/users (admin only).
func (h *Handler) CreateUser(c *gin.Context) {
	identity, ok := IdentityFrom(c)
	if !ok {
		unauthorized(c, "authentication is required")
		return
	}

	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBindError(c, err)
		return
	}

	out, err := h.users.Create(c.Request.Context(), identity, app.CreateUserInput{
		Name:     req.Name,
		Email:    req.Email,
		Password: req.Password,
		Role:     req.Role,
	})
	if err != nil {
		respondError(c, err)
		return
	}

	c.Header("Location", "/api/v1/users/"+out.ID)
	c.JSON(http.StatusCreated, newUserResponse(out))
}

// GetUser handles GET /api/v1/users/:id (the user itself, or an admin).
func (h *Handler) GetUser(c *gin.Context) {
	identity, ok := IdentityFrom(c)
	if !ok {
		unauthorized(c, "authentication is required")
		return
	}

	out, err := h.users.GetByID(c.Request.Context(), identity, c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, newUserResponse(out))
}

// GetMe handles GET /api/v1/users/me, the profile of the token's holder.
//
// It exists so a client can read its own profile without decoding sub out
// of the token itself.
func (h *Handler) GetMe(c *gin.Context) {
	identity, ok := IdentityFrom(c)
	if !ok {
		unauthorized(c, "authentication is required")
		return
	}

	out, err := h.users.GetByID(c.Request.Context(), identity, identity.UserID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, newUserResponse(out))
}

// ListUsers handles GET /api/v1/users (admin only).
func (h *Handler) ListUsers(c *gin.Context) {
	identity, ok := IdentityFrom(c)
	if !ok {
		unauthorized(c, "authentication is required")
		return
	}

	var q listQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		respondBindError(c, err)
		return
	}

	limit := q.limitOrDefault()
	out, err := h.users.List(c.Request.Context(), identity, app.ListInput{
		Limit:  limit,
		Cursor: q.Cursor,
	})
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, newListResponse(out, limit))
}

// UpdateUser handles PATCH /api/v1/users/:id (the user itself, or an admin).
func (h *Handler) UpdateUser(c *gin.Context) {
	identity, ok := IdentityFrom(c)
	if !ok {
		unauthorized(c, "authentication is required")
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBindError(c, err)
		return
	}

	out, err := h.users.Update(c.Request.Context(), identity, c.Param("id"), req.toInput())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, newUserResponse(out))
}

// DeleteUser handles DELETE /api/v1/users/:id (admin only).
func (h *Handler) DeleteUser(c *gin.Context) {
	identity, ok := IdentityFrom(c)
	if !ok {
		unauthorized(c, "authentication is required")
		return
	}

	if err := h.users.Delete(c.Request.Context(), identity, c.Param("id")); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- health ---

// Healthz is the liveness probe: it reports that the process is running.
//
// It deliberately does not touch the database. If MongoDB is briefly down,
// we do not want the orchestrator killing pods over an external dependency.
func (h *Handler) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{Status: "ok", Version: h.version})
}

// Readyz is the readiness probe: it reports whether we can serve traffic.
//
// It does check the database, because without MongoDB the service cannot do
// its job and should be pulled out of the load balancer for the time being.
func (h *Handler) Readyz(c *gin.Context) {
	if h.pinger == nil {
		c.JSON(http.StatusOK, healthResponse{Status: "ok", Version: h.version})
		return
	}

	if err := h.pinger.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, healthResponse{
			Status:  "unavailable",
			Mongo:   "down",
			Version: h.version,
		})
		return
	}
	c.JSON(http.StatusOK, healthResponse{Status: "ok", Mongo: "up", Version: h.version})
}
