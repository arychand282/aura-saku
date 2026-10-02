package handlers

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"aura-saku/internal/auth"
	"aura-saku/internal/models"
	"aura-saku/pkg/httpresponse"
)

// dummyHash is compared against on a failed email lookup so a nonexistent
// admin email doesn't respond measurably faster than a wrong password would
// (bcrypt is deliberately slow; skipping it entirely on a DB miss would leak
// which emails have admin accounts via response timing).
const dummyHash = "$2a$10$7XLKe0HPMU5YuVHQ5bx9o.Gczr.K4w1VCEUfPev0/zJYNAYHlkaDi"

type AuthHandler struct {
	db            *gorm.DB
	redisClient   *redis.Client
	jwtSecret     string
	sessionTTL    time.Duration
	secureCookies bool
}

// secureCookies should be true whenever the app is actually reachable over
// HTTPS (config.Config.AppEnv == "production") and false for local
// dev/testing over plain HTTP, where a Secure cookie would never be sent
// back by the browser at all.
func NewAuthHandler(db *gorm.DB, redisClient *redis.Client, jwtSecret string, sessionTTL time.Duration, secureCookies bool) *AuthHandler {
	return &AuthHandler{db: db, redisClient: redisClient, jwtSecret: jwtSecret, sessionTTL: sessionTTL, secureCookies: secureCookies}
}

type verifyLoginTokenRequest struct {
	Token string `json:"token"`
}

// VerifyLoginToken exchanges a bot-issued one-time token (see
// internal/service/bot's /dashboard command) for a session.
func (h *AuthHandler) VerifyLoginToken(c *fiber.Ctx) error {
	var req verifyLoginTokenRequest
	if err := c.BodyParser(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		return httpresponse.Error(c, fiber.StatusBadRequest, "token is required")
	}

	userID, err := auth.ConsumeLoginToken(c.Context(), h.redisClient, req.Token)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusUnauthorized, "invalid or expired token")
	}

	sessionToken, err := auth.CreateSession(c.Context(), h.redisClient, h.jwtSecret, h.sessionTTL, auth.RoleUser, userID, 0)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to create session")
	}

	setSessionCookie(c, sessionToken, h.sessionTTL, h.secureCookies)
	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{"role": auth.RoleUser, "user_id": userID})
}

type adminLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AdminLogin authenticates a dashboard operator by email/password.
func (h *AuthHandler) AdminLogin(c *fiber.Ctx) error {
	var req adminLoginRequest
	if err := c.BodyParser(&req); err != nil || req.Email == "" || req.Password == "" {
		return httpresponse.Error(c, fiber.StatusBadRequest, "email and password are required")
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	var admin models.Admin
	err := h.db.Where("email = ? AND is_active = ?", email, true).First(&admin).Error
	if err != nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(req.Password))
		return httpresponse.Error(c, fiber.StatusUnauthorized, "invalid email or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(req.Password)); err != nil {
		return httpresponse.Error(c, fiber.StatusUnauthorized, "invalid email or password")
	}

	role := auth.RoleAdmin
	if admin.Role == models.AdminRoleSuperadmin {
		role = auth.RoleSuperadmin
	}

	sessionToken, err := auth.CreateSession(c.Context(), h.redisClient, h.jwtSecret, h.sessionTTL, role, 0, admin.ID)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to create session")
	}

	setSessionCookie(c, sessionToken, h.sessionTTL, h.secureCookies)
	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{"role": role, "name": admin.Name})
}

// Logout revokes the current session server-side (so the token is dead
// immediately, not just once it naturally expires) and clears the cookie.
func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	if tokenString := c.Cookies(auth.SessionCookieName); tokenString != "" {
		if claims, err := auth.VerifySession(c.Context(), h.redisClient, h.jwtSecret, tokenString); err == nil {
			_ = auth.RevokeSession(c.Context(), h.redisClient, claims.SessionID)
		}
	}
	c.ClearCookie(auth.SessionCookieName)
	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{"message": "logged out"})
}

func setSessionCookie(c *fiber.Ctx, token string, ttl time.Duration, secure bool) {
	c.Cookie(&fiber.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Expires:  time.Now().Add(ttl),
		HTTPOnly: true,
		SameSite: "Lax",
		Secure:   secure,
	})
}
