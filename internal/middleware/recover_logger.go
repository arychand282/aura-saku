package middleware

import (
	"github.com/gofiber/fiber/v2"
	fiberlogger "github.com/gofiber/fiber/v2/middleware/logger"
	fiberrecover "github.com/gofiber/fiber/v2/middleware/recover"
)

// Recover wraps every request with panic recovery so a single bad request
// can never crash the whole server.
func Recover() fiber.Handler {
	return fiberrecover.New()
}

// RequestLogger logs every request's method, path, status, and latency.
func RequestLogger() fiber.Handler {
	return fiberlogger.New(fiberlogger.Config{
		Format: "[${time}] ${status} - ${method} ${path} (${latency})\n",
	})
}
