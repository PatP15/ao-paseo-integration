package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoreporter"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ao-paseo-reporter: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("expected emit or serve")
	}
	dataDir, err := paseoreporter.DefaultDataDir()
	if err != nil {
		return err
	}
	switch args[0] {
	case "emit":
		flags := flag.NewFlagSet("emit", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		flags.StringVar(&dataDir, "data-dir", dataDir, "AO reporter state directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("emit takes JSON on stdin and no arguments")
		}
		payload, err := io.ReadAll(io.LimitReader(stdin, 4097))
		if err != nil {
			return fmt.Errorf("read report: %w", err)
		}
		return paseoreporter.Emit(dataDir, payload)
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var sessionID, launchID, nonce string
		flags.StringVar(&dataDir, "data-dir", dataDir, "AO reporter state directory")
		flags.StringVar(&sessionID, "session", "", "AO session id")
		flags.StringVar(&launchID, "launch", "", "AO launch id")
		flags.StringVar(&nonce, "nonce", "", "AO report nonce")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(launchID) == "" {
			return fmt.Errorf("serve requires --session, --launch, and --nonce")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := paseoreporter.Serve(ctx, dataDir, sessionID, launchID, nonce, stdout)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
