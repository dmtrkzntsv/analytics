package store

import "errors"

// Registry outcomes a caller can act on, as opposed to storage failures
// it can only report. Backends wrap these with %w so the message still
// names the row ("rename: unknown alias \"blog\": not found") while
// errors.Is answers the question the edge actually has: does the caller
// need to pick a different alias, or did the database break?
var (
	// ErrNotFound: the project or key named by the call does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict: the alias or key label is already taken.
	ErrConflict = errors.New("already exists")
)
