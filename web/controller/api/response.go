package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ErrorResp is the JSON envelope returned for any API failure.
type ErrorResp struct {
	Error   bool   `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// OK writes a 200 with {data: ...}. nil data becomes {data: null}.
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// Created writes a 201.
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, gin.H{"data": data})
}

// NoContent writes a 204.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Fail aborts with a structured error envelope.
func Fail(c *gin.Context, status int, code, message string, details any) {
	c.AbortWithStatusJSON(status, ErrorResp{
		Error:   true,
		Code:    code,
		Message: message,
		Details: details,
	})
}

// BadRequest is the most common client-error helper.
func BadRequest(c *gin.Context, code, message string) {
	Fail(c, http.StatusBadRequest, code, message, nil)
}

// NotFound helper.
func NotFound(c *gin.Context, code, message string) {
	Fail(c, http.StatusNotFound, code, message, nil)
}

// Internal is for unexpected backend errors. The error message is included
// because this panel is operator-facing — adjust if that becomes a leak.
func Internal(c *gin.Context, code string, err error) {
	msg := "internal error"
	if err != nil {
		msg = err.Error()
	}
	Fail(c, http.StatusInternalServerError, code, msg, nil)
}
