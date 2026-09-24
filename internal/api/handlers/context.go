package handlers

import (
	"github.com/gofiber/fiber/v2"

	"aura-saku/internal/middleware"
)

// effectiveUserID reads the user_id resolved by middleware.RequireSession —
// a user's own ID for a "user" session, or the validated ?user_id= override
// for an admin/superadmin session. By the time a handler runs, this is
// already authenticated; handlers must never fall back to reading
// c.Query("user_id") directly.
func effectiveUserID(c *fiber.Ctx) uint {
	id, _ := c.Locals(middleware.LocalsEffectiveUserID).(uint)
	return id
}
