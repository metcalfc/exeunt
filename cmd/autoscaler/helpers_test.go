package main

import "log/slog"

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(nil, nil))
}
