// Package errs defines the error taxonomy shared by every layer. Each kind
// maps to one process exit code, so scripts can branch on the failure class
// without parsing messages.
package errs

import (
	"errors"
	"fmt"
)

type Code string

const (
	CodeUsage      Code = "usage"
	CodeAuth       Code = "auth"
	CodeValidation Code = "validation"
	CodeNotFound   Code = "not_found"
	CodePermission Code = "permission"
	CodeServer     Code = "server"
	CodeNetwork    Code = "network"
	CodeInternal   Code = "internal"
)

var exitCodes = map[Code]int{
	CodeUsage:      1,
	CodeAuth:       2,
	CodeValidation: 3,
	CodeNotFound:   4,
	CodePermission: 5,
	CodeServer:     6,
	CodeNetwork:    7,
	CodeInternal:   1,
}

// Error is the only error type commands should return. Hint is a single
// actionable sentence; Details carries the raw service response for -v and
// the JSON envelope.
type Error struct {
	Code    Code
	Message string
	Hint    string
	Details any
	Request *RequestInfo
	cause   error
}

// RequestInfo is attached to HTTP failures so --verbose can show exactly what
// was sent without the command layer knowing about HTTP.
type RequestInfo struct {
	Method    string
	URL       string
	Status    int
	RequestID string
	Body      string
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

func (e *Error) ExitCode() int {
	if c, ok := exitCodes[e.Code]; ok {
		return c
	}
	return 1
}

func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func Wrap(code Code, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), cause: err}
}

func Usage(format string, args ...any) *Error { return New(CodeUsage, format, args...) }

func NotFound(format string, args ...any) *Error { return New(CodeNotFound, format, args...) }

func (e *Error) WithHint(h string) *Error {
	e.Hint = h
	return e
}

// As returns err as *Error, classifying anything foreign as internal so the
// reporter always has a code to work with.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error(), cause: err}
}
