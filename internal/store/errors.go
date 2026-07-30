package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// Kind classifies a store error so the HTTP layer can pick a status code
// without matching on error strings.
type Kind string

const (
	KindInternal     Kind = "internal"
	KindInvalid      Kind = "invalid"      // 400: the request never made sense
	KindNotFound     Kind = "not_found"    // 404
	KindConflict     Kind = "conflict"     // 409: the world moved under the caller
	KindPrecondition Kind = "precondition" // 422: valid request, wrong moment
)

// Error is the store's public error type.
type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Msg)
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf reports how an error should be surfaced. Anything unrecognised is
// internal, which is the safe default: it gets logged and returns a 500.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if err == nil {
		return ""
	}
	return KindInternal
}

// Message returns a caller-safe message, or "" if the error should not be shown.
func Message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return ""
}

func invalid(format string, args ...any) error {
	return &Error{Kind: KindInvalid, Msg: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) error {
	return &Error{Kind: KindNotFound, Msg: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &Error{Kind: KindConflict, Msg: fmt.Sprintf(format, args...)}
}

func precondition(format string, args ...any) error {
	return &Error{Kind: KindPrecondition, Msg: fmt.Sprintf(format, args...)}
}

// constraintConflict turns a unique-violation into a conflict, because the two
// unique indexes that matter here (one active match per court, one active match
// per player) are exactly the races this app is designed to lose safely.
func constraintConflict(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "one_active_match_per_court":
		return &Error{Kind: KindConflict, Msg: "that court already has a live match", Err: err}
	case "one_active_match_per_player":
		return &Error{Kind: KindConflict, Msg: "one of those players is already in a live match", Err: err}
	}
	if pgErr.Code == "23505" {
		return &Error{Kind: KindConflict, Msg: "that record already exists", Err: err}
	}
	return err
}
