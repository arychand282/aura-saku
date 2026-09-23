package apperror

import "errors"

var (
	ErrNotImplemented    = errors.New("not implemented")
	ErrRateLimited       = errors.New("rate limit exceeded")
	ErrQuotaExceeded     = errors.New("daily AI quota exceeded")
	ErrInputTooLong      = errors.New("input exceeds maximum allowed length")
	ErrAmbiguousInput    = errors.New("input is ambiguous, clarification required")
	ErrUnsupportedProvider = errors.New("unsupported provider")
	ErrNoUndoableTransaction = errors.New("no transaction available to undo")
)
