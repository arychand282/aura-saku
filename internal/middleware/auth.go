package middleware

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"aura-saku/internal/auth"
	"aura-saku/internal/models"
	"aura-saku/pkg/httpresponse"
)

// Locals keys set by RequireSession for handlers to read.
const (
	LocalsRole            = "role"
	LocalsEffectiveUserID = "effectiveUserID"
	LocalsAdminID         = "adminID"
)

// RequireSession verifies the session cookie (or an Authorization: Bearer
// header, for non-browser API clients) and resolves the effective user_id
// downstream handlers should act on:
//   - a "user" session is locked to its own ID — any ?user_id= a client
//     passes is ignored;
//   - an admin/superadmin session may pass ?user_id= to act on behalf of
//     any user (support/ops access), and is additionally re-checked against
//     Postgres's Admin.IsActive on every request so deactivating an admin
//     takes effect immediately, not just after their session naturally expires.
func RequireSession(redisClient *redis.Client, db *gorm.DB, jwtSecret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tokenString := extractSessionToken(c)
		if tokenString == "" {
			return httpresponse.Error(c, fiber.StatusUnauthorized, "authentication required")
		}

		claims, err := auth.VerifySession(c.Context(), redisClient, jwtSecret, tokenString)
		if err != nil {
			return httpresponse.Error(c, fiber.StatusUnauthorized, "invalid or expired session")
		}

		switch claims.Role {
		case auth.RoleAdmin, auth.RoleSuperadmin:
			var admin models.Admin
			if err := db.First(&admin, claims.AdminID).Error; err != nil || !admin.IsActive {
				return httpresponse.Error(c, fiber.StatusUnauthorized, "account is no longer active")
			}

			userIDParam := c.Query("user_id")
			if userIDParam == "" {
				return httpresponse.Error(c, fiber.StatusBadRequest, "user_id query param is required")
			}
			parsed, err := strconv.ParseUint(userIDParam, 10, 64)
			if err != nil {
				return httpresponse.Error(c, fiber.StatusBadRequest, "invalid user_id")
			}
			c.Locals(LocalsEffectiveUserID, uint(parsed))
			c.Locals(LocalsAdminID, claims.AdminID)
		default: // RoleUser
			c.Locals(LocalsEffectiveUserID, claims.UserID)
		}

		c.Locals(LocalsRole, claims.Role)
		return c.Next()
	}
}

func extractSessionToken(c *fiber.Ctx) string {
	if cookie := c.Cookies(auth.SessionCookieName); cookie != "" {
		return cookie
	}
	if h := c.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}
