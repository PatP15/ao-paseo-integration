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
		return runMaintain(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// runMaintain drives the host maintenance channel's worker half (U9/U9a).
// Every verb takes --nonce (issued fresh by AO per invocation) and emits
// framed events on stdout for terminal capture; file paths come from
// allowlists or explicit flags, never patterns.
func runMaintain(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("maintain expects a verb")
	}
	verb := args[0]
	flags := flag.NewFlagSet("maintain "+verb, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var nonce, skillsDir, prefsPath, contentB64, contentSHA, expectSHA string
	var target, repoPath, baseBranch, skillName string
	flags.StringVar(&nonce, "nonce", "", "AO channel nonce for this invocation")
	flags.StringVar(&skillsDir, "skills-dir", "", "skills directory (default ~/.claude/skills)")
	flags.StringVar(&prefsPath, "prefs-path", "", "preferences file (default ~/.paseo/orchestration-preferences.json)")
	flags.StringVar(&contentB64, "content-b64", "", "write verbs: new file content, base64")
	flags.StringVar(&contentSHA, "sha256", "", "write verbs: hex sha256 of the new content")
	flags.StringVar(&expectSHA, "expect-sha256", "", "write verbs: hex sha256 of the content currently on disk")
	flags.StringVar(&target, "target", "", "file verbs: allowlisted file target, e.g. machine-claude")
	flags.StringVar(&repoPath, "repo", "", "repo verbs: absolute checkout path on this host")
	flags.StringVar(&baseBranch, "base", "", "repo-status: base branch to hash instruction files at")
	flags.StringVar(&skillName, "name", "", "skill verbs: bare skill directory name")
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
	case "file-read":
		return paseoreporter.MaintainFileRead(target, nonce, stdout)
	case "file-write":
		return paseoreporter.MaintainFileWrite(target, nonce, expectSHA, stdin, stdout)
	case "repo-status":
		if baseBranch == "" {
			return fmt.Errorf("maintain repo-status requires --base")
		}
		return paseoreporter.MaintainRepoStatus(repoPath, baseBranch, nonce, stdout)
	case "repo-ff":
		return paseoreporter.MaintainRepoFF(repoPath, nonce, stdout)
	case "skill-read":
		return paseoreporter.MaintainSkillRead(skillsDir, skillName, nonce, stdout)
	case "skill-push":
		return paseoreporter.MaintainSkillPush(skillsDir, skillName, nonce, stdin, stdout)
	default:
		return fmt.Errorf("unknown maintain verb %q", verb)
	}
}
