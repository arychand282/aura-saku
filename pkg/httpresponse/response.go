package httpresponse

import "github.com/gofiber/fiber/v2"

// Success writes a standard {"data": ...} envelope with the given status code.
func Success(c *fiber.Ctx, status int, data any) error {
	return c.Status(status).JSON(fiber.Map{"data": data})
}

// Error writes a standard {"error": ...} envelope with the given status code.
func Error(c *fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{"error": message})
}
