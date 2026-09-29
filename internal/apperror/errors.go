package apperror

import "errors"

var (
	ErrNotImplemented           = errors.New("not implemented")
	ErrRateLimited              = errors.New("rate limit exceeded")
	ErrQuotaExceeded            = errors.New("daily AI quota exceeded")
	ErrInputTooLong             = errors.New("input exceeds maximum allowed length")
	ErrAmbiguousInput           = errors.New("input is ambiguous, clarification required")
	ErrUnsupportedProvider      = errors.New("unsupported provider")
	ErrNoUndoableTransaction    = errors.New("no transaction available to undo")
	ErrProviderAudioUnsupported = errors.New("this AI provider does not support audio transcription")
	ErrInvalidSession           = errors.New("invalid or expired session")
	ErrInvalidLoginToken        = errors.New("invalid or expired login token")
	ErrInvalidPeriod            = errors.New("invalid period, must be one of: daily, weekly, monthly, yearly")
	ErrUnhandledUpdateType      = errors.New("webhook update has no actionable message (e.g. an edited message, callback query, or chat-member change)")
	ErrRecurringNotFound        = errors.New("no active recurring transaction found with that id for this user")
)
