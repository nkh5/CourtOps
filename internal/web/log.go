package web

import "log/slog"

// slogLevel exists so the request logger can pick a level without importing
// slog constants at every call site.
type slogLevel = slog.Level

const (
	levelInfo  = slog.LevelInfo
	levelWarn  = slog.LevelWarn
	levelError = slog.LevelError
)
