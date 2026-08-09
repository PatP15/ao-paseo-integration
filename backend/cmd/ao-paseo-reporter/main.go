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
	case "maintain":
		return runMaintain(args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// runMaintain drives the U9 host maintenance channel's worker half. Every verb
// takes --nonce (issued fresh by AO per invocation) and emits framed events on
// stdout for terminal capture; paths default to the AO-owned locations and are
// overridable for tests, never patterns.
func runMaintain(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("maintain expects inventory, prefs-read, or prefs-write")
	}
	verb := args[0]
	flags := flag.NewFlagSet("maintain "+verb, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var nonce, skillsDir, prefsPath, contentB64, contentSHA, expectSHA string
	flags.StringVar(&nonce, "nonce", "", "AO channel nonce for this invocation")
	flags.StringVar(&skillsDir, "skills-dir", "", "skills directory (default ~/.claude/skills)")
	flags.StringVar(&prefsPath, "prefs-path", "", "preferences file (default ~/.paseo/orchestration-preferences.json)")
	flags.StringVar(&contentB64, "content-b64", "", "prefs-write: new file content, base64")
	flags.StringVar(&contentSHA, "sha256", "", "prefs-write: hex sha256 of the new content")
	flags.StringVar(&expectSHA, "expect-sha256", "", "prefs-write: hex sha256 of the content currently on disk")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(nonce) == "" {
		return fmt.Errorf("maintain %s requires --nonce and no arguments", verb)
	}
	if skillsDir == "" {
		var err error
		if skillsDir, err = paseoreporter.DefaultSkillsDir(); err != nil {
			return err
		}
	}
	if prefsPath == "" {
		var err error
		if prefsPath, err = paseoreporter.DefaultPrefsPath(); err != nil {
			return err
		}
	}
	switch verb {
	case "inventory":
		return paseoreporter.MaintainInventory(skillsDir, nonce, stdout)
	case "prefs-read":
		return paseoreporter.MaintainPrefsRead(prefsPath, nonce, stdout)
	case "prefs-write":
		if contentB64 == "" || contentSHA == "" {
			return fmt.Errorf("maintain prefs-write requires --content-b64 and --sha256")
		}
		return paseoreporter.MaintainPrefsWrite(prefsPath, nonce, contentB64, contentSHA, expectSHA, stdout)
	default:
		return fmt.Errorf("unknown maintain verb %q", verb)
	}
}
