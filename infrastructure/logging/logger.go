package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

const LoggerTypeJson = "json"
const LoggerTypeSimple = "simple"
const LoggerTypePrettyJson = "pretty-json"
const LoggerTypeColor = "color"

var Logger = CLILogger

// Ensure the process-wide logger implements the port contract.
var _ contract.Logger = Logger

var CLILogger = slog.New(NewCliHandler(os.Stdout, &slog.HandlerOptions{}))
var ColorLogger = slog.New(NewColorHandler(os.Stdout, nil))
var JsonLogger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{}))
var PrettyJson = slog.New(NewPrettyJSONHandler(os.Stdout, nil))

func Init(logger string, level string) {
	var slogLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: slogLevel}
	switch logger {
	case LoggerTypePrettyJson:
		Logger = slog.New(NewPrettyJSONHandler(os.Stdout, opts))
	case LoggerTypeJson:
		Logger = slog.New(slog.NewJSONHandler(os.Stdout, opts))
	case LoggerTypeColor:
		Logger = slog.New(NewColorHandler(os.Stdout, opts))
	case LoggerTypeSimple:
		Logger = slog.New(NewCliHandler(os.Stdout, opts))
	default:
		Logger = slog.New(NewCliHandler(os.Stdout, opts))
	}
}

func Log(level slog.Level, event string, message string) {
	ctx := context.Background()
	fnc := Logger.InfoContext
	switch level {
	case slog.LevelError:
		fnc = Logger.ErrorContext
	case slog.LevelWarn:
		fnc = Logger.WarnContext
	case slog.LevelDebug:
		fnc = Logger.DebugContext
	}
	fnc(
		ctx,
		event,
		slog.String("message", message),
	)
}

func Info(event string, message string) {
	Log(slog.LevelInfo, event, message)
}

func Warn(event string, message string) {
	Log(slog.LevelWarn, event, message)
}

func Error(event string, message string) {
	Log(slog.LevelError, event, message)
}
