package commands

import (
	"context"
	"log/slog"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/logging"
)

var Version = "dev"

// NewApp returns the root CLI command with all subcommands registered.
func NewApp() *cli.Command {
	return &cli.Command{
		Name:    "veil",
		Usage:   "Track, transform, and render deployment configuration",
		Version: Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "Log level (debug, info, warn, error)",
				Value:   "info",
				Sources: cli.EnvVars("VEIL_LOG_LEVEL"),
			},
			&cli.StringSliceFlag{
				Name:    "log-paths",
				Usage:   "Additional log destinations: \"stdout\", \"stderr\", or a file path (repeatable)",
				Sources: cli.EnvVars("VEIL_LOG_PATHS"),
			},
			&cli.StringFlag{
				Name:    "output",
				Usage:   "Output format: \"pretty\" or \"json\"",
				Value:   defaultOutputFormat(),
				Sources: cli.EnvVars("VEIL_OUTPUT"),
			},
			&cli.BoolFlag{
				Name:    "quiet",
				Usage:   "Suppress all log output to stdout/stderr",
				Sources: cli.EnvVars("VEIL_QUIET"),
			},
		},
		Before: func(ctx context.Context, command *cli.Command) (context.Context, error) {
			output := command.String("output")
			interact.SetOutputFormat(output)
			interact.SetDefault(interact.NewPrinter(command.Root().Writer))

			level := parseLogLevel(command.String("log-level"))
			logPaths := command.StringSlice("log-paths")
			quiet := command.Bool("quiet")

			if quiet {
				logPaths = nil
			} else if len(logPaths) == 0 && output == interact.OutputJSON {
				logPaths = []string{"stdout"}
			}

			cleanup, err := logging.Setup(level, logPaths, command.Root().Writer, command.Root().ErrWriter)
			if err != nil {
				slog.Warn("Failed to set up log file", "error", err)
			}
			_ = cleanup

			return ctx, nil
		},
		Commands: []*cli.Command{
			Render(),
			Gen(),
			New(),
			Build(),
			Validate(),
			Eject(),
			Schema(),
		},
	}
}

// defaultOutputFormat returns "json" for agents and "pretty" for humans.
func defaultOutputFormat() string {
	if interact.IsJSON() {
		return interact.OutputJSON
	}
	return interact.OutputPretty
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
