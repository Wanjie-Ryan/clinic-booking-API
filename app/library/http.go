package library

import (
	"github.com/labstack/echo/v4"
)

func RespondRaw(c echo.Context, code int, payload interface{}) error {
	return c.JSON(code, payload)
}
