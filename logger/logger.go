package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarning
	LevelError
)

var (
	leveler = new(slog.LevelVar)
	logger  *slog.Logger
)

func init() {
	leveler.Set(slog.LevelInfo)
	logger = newLogger()
}

func newLogger() *slog.Logger {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: leveler,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey:
				return slog.String(slog.TimeKey, a.Value.Time().Format("2006/01/02 15:04:05"))
			case slog.SourceKey:
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(handler).With("app", "x-ui")
}

func InitLogger(level Level) {
	switch level {
	case LevelDebug:
		leveler.Set(slog.LevelDebug)
	case LevelInfo:
		leveler.Set(slog.LevelInfo)
	case LevelWarning:
		leveler.Set(slog.LevelWarn)
	case LevelError:
		leveler.Set(slog.LevelError)
	default:
		leveler.Set(slog.LevelInfo)
	}
}

func joinArgs(args []interface{}) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = fmt.Sprint(a)
	}
	return strings.Join(parts, " ")
}

func log(level slog.Level, msg string) {
	logger.Log(context.Background(), level, msg)
}

func Debug(args ...interface{})                 { log(slog.LevelDebug, joinArgs(args)) }
func Debugf(format string, args ...interface{}) { log(slog.LevelDebug, fmt.Sprintf(format, args...)) }
func Info(args ...interface{})                  { log(slog.LevelInfo, joinArgs(args)) }
func Infof(format string, args ...interface{})  { log(slog.LevelInfo, fmt.Sprintf(format, args...)) }
func Warning(args ...interface{})               { log(slog.LevelWarn, joinArgs(args)) }
func Warningf(format string, args ...interface{}) {
	log(slog.LevelWarn, fmt.Sprintf(format, args...))
}
func Error(args ...interface{})                 { log(slog.LevelError, joinArgs(args)) }
func Errorf(format string, args ...interface{}) { log(slog.LevelError, fmt.Sprintf(format, args...)) }

// Now returns the current time honoring system clock; exported for tests.
func Now() time.Time { return time.Now() }
