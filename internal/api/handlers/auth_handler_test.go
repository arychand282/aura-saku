package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// TestSetSessionCookie_SecureFlagMatchesParameter covers the fix for the
// "set Secure: true once deployed behind real HTTPS" TODO: the cookie's
// Secure attribute must follow whatever secureCookies the caller passes
// (ultimately config.Config.AppEnv == "production", wired in
// internal/api/router.go), not be hardcoded either way — false still works
// for local dev/testing over plain HTTP, true once actually behind HTTPS.
func TestSetSessionCookie_SecureFlagMatchesParameter(t *testing.T) {
	for _, secure := range []bool{false, true} {
		app := fiber.New()
		app.Get("/x", func(c *fiber.Ctx) error {
			setSessionCookie(c, "tok", time.Hour, secure)
			return c.SendStatus(fiber.StatusOK)
		})

		resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/x", nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}

		cookieHeader := strings.Join(resp.Header.Values("Set-Cookie"), "; ")
		hasSecure := strings.Contains(strings.ToLower(cookieHeader), "secure")
		if hasSecure != secure {
			t.Errorf("secure=%v: Set-Cookie header %q, want Secure attribute present=%v", secure, cookieHeader, secure)
		}
	}
}
