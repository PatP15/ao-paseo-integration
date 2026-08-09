package paseo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type fakeRunner struct {
	results []commandResult
	errs    []error
	calls   [][]string
}

func (f *fakeRunner) Run(_ context.Context, args []string) (commandResult, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	index := len(f.calls) - 1
	var result commandResult
	if index < len(f.results) {
		result = f.results[index]
	}
	if index < len(f.errs) {
		return result, f.errs[index]
	}
	return result, nil
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	path := filepath.Join(filepath.Dir(file), "../../../../../docs/paseo-integration/spike/fixtures", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestFixtureContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func([]byte) error
	}{
		{"s1a-run.json", func(data []byte) error { _, err := decodeStrict[RunResult](data); return err }},
		{"s1a-inspect.json", func(data []byte) error { _, err := decodeStrict[AgentDetail](data); return err }},
		{"s2-ls-by-intent.json", func(data []byte) error { _, err := decodeStrict[[]Agent](data); return err }},
		{"s2-ls-malformed.json", func(data []byte) error { _, err := decodeStrict[[]Agent](data); return err }},
		{"s3-workspace-create.json", func(data []byte) error { _, err := decodeStrict[Workspace](data); return err }},
		{"s3-workspace-ls.json", func(data []byte) error { _, err := decodeStrict[[]Workspace](data); return err }},
		{"s9-provider-ls.json", func(data []byte) error { _, err := decodeStrict[[]Provider](data); return err }},
		{"s1f-terminal-capture.json", func(data []byte) error { _, err := decodeStrict[TerminalCapture](data); return err }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.run(fixture(t, test.name)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStrictJSONRejectsDriftAndTrailingValues(t *testing.T) {
	t.Parallel()
	if _, err := decodeStrict[Workspace]([]byte(`{"workspaceId":"w","unknown":true}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := decodeStrict[Workspace]([]byte(`{"workspaceId":"w"} {"workspaceId":"x"}`)); err == nil {
		t.Fatal("trailing JSON value accepted")
	}
}

func TestClientPinsVersionAndRecordsIt(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{results: []commandResult{{stdout: []byte("paseo 0.2.5\n")}}}
	client, err := newClient(context.Background(), "worker:6767", run)
	if err != nil {
		t.Fatal(err)
	}
	if client.Version() != SupportedVersion {
		t.Fatalf("version = %q", client.Version())
	}
	if !reflect.DeepEqual(run.calls, [][]string{{"--version"}}) {
		t.Fatalf("calls = %#v", run.calls)
	}
}

func TestClientRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{results: []commandResult{{stdout: []byte("0.3.0\n")}}}
	_, err := newClient(context.Background(), "worker:6767", run)
	if !IsKind(err, ErrorUnsupportedVersion) {
		t.Fatalf("error = %v", err)
	}
}

func TestCommandsPutHostOnSubcommandAndForceGlobalArchivedList(t *testing.T) {
	t.Parallel()
	args, err := listAgentsArgs("worker:6767", "ao.intent=intent-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ls", "--host", "worker:6767", "-a", "-g", "--json", "--label", "ao.intent=intent-1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if args[0] == "--host" {
		t.Fatal("--host emitted as a global option")
	}
}

func TestValidationHappensBeforeExec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		host  string
		label string
	}{
		{"colonless host", "worker", "ao.intent=x"},
		{"missing equals", "worker:6767", "ao.intent"},
		{"two equals", "worker:6767", "ao.intent=x=y"},
		{"empty key", "worker:6767", "=x"},
		{"empty value", "worker:6767", "ao.intent="},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := listAgentsArgs(test.host, test.label); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestRunRejectsDuplicateLabelKeys(t *testing.T) {
	t.Parallel()
	_, err := runArgs("worker:6767", RunRequest{
		WorkspaceID: "wks_1", Provider: "codex", Prompt: "work",
		Labels: []string{"ao.intent=one", "ao.intent=two"},
	})
	if err == nil {
		t.Fatal("duplicate label key accepted")
	}
}

func TestStopAndDeleteBanAll(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"stop", "delete"} {
		if _, err := destructiveArgs("worker:6767", operation, "--all"); err == nil {
			t.Fatalf("%s --all accepted", operation)
		}
		if _, err := destructiveArgs("worker:6767", operation, "agent-1", "--all"); err == nil {
			t.Fatalf("%s id --all accepted", operation)
		}
	}
}

func TestPositionalIDsRejectFlagInjection(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func() error{
		"host":            func() error { _, err := listAgentsArgs("--all:6767", ""); return err },
		"inspect":         func() error { _, err := inspectArgs("worker:6767", "--all"); return err },
		"stop short flag": func() error { _, err := destructiveArgs("worker:6767", "stop", "-a"); return err },
		"capture":         func() error { _, err := terminalCaptureArgs("worker:6767", "--all", 0, 1); return err },
		"workspace": func() error {
			_, err := workspaceCreateArgs("worker:6767", WorkspaceCreateRequest{
				RepoPath: "/repo", Branch: "--help", BaseBranch: "main", WorktreeSlug: "task", Title: "task",
			})
			return err
		},
		"run provider": func() error {
			_, err := runArgs("worker:6767", RunRequest{WorkspaceID: "w", Provider: "--help", Prompt: "work"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := build(); err == nil {
				t.Fatal("flag-shaped value was accepted")
			}
		})
	}
}

func TestFreeFormPromptUsesOptionTerminatorWhenFlagShaped(t *testing.T) {
	t.Parallel()
	args, err := runArgs("worker:6767", RunRequest{WorkspaceID: "w", Provider: "claude", Prompt: "- review this"})
	if err != nil {
		t.Fatal(err)
	}
	if got := args[len(args)-2:]; !reflect.DeepEqual(got, []string{"--", "- review this"}) {
		t.Fatalf("argv tail = %v", got)
	}
}

func TestScrubPaseoEnv(t *testing.T) {
	t.Parallel()
	got := scrubPaseoEnv([]string{
		"PATH=/bin", "PASEO_HOST=operator:6767", "PASEO_PASSWORD=secret",
		"PASEO_AGENT_ID=parent", "PASEO_FUTURE_KEY=value", "NOT_PASEO=x",
	})
	want := []string{"PATH=/bin", "NOT_PASEO=x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}

func TestErrorsAreClassifiedAndRedacted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		detail string
		kind   ErrorKind
	}{
		{"network", "connection refused", ErrorNetwork},
		{"auth", "401 unauthorized password=topsecret", ErrorAuth},
		{"invalid", "unknown option --wat", ErrorInvalidRequest},
		{"provider", "provider codex is disabled", ErrorProviderUnavailable},
		{"workspace", "workspace could not be created", ErrorWorkspace},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := commandError(errors.New("exit 1"), commandResult{stderr: []byte(test.detail)}, "tcp://worker:6767?password=topsecret")
			if !IsKind(err, test.kind) {
				t.Fatalf("kind for %q = %v", test.detail, err)
			}
			message := err.Error()
			if strings.Contains(message, "topsecret") || strings.Contains(message, "#offer=abc") {
				t.Fatalf("secret leaked: %s", message)
			}
		})
	}

	err := commandError(errors.New("exit 1"), commandResult{
		stderr: []byte("connect https://app.paseo.sh/#offer=abc_DEF and password=hunter2"),
	})
	if strings.Contains(err.Error(), "abc_DEF") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("offer or password leaked: %s", err)
	}
}

func TestStatusMappingRejectsUnknownValues(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"initializing", "idle", "running", "error", "closed"} {
		if _, err := mapStatus(status); err != nil {
			t.Fatalf("map %q: %v", status, err)
		}
	}
	if _, err := mapStatus("completed"); err == nil {
		t.Fatal("unknown completed state accepted")
	}
}

func TestFixtureDetailMapsToAONeutralFacts(t *testing.T) {
	t.Parallel()
	detail, err := decodeStrict[AgentDetail](fixture(t, "s1a-inspect.json"))
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := mapAgentDetail("host-1", detail)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.HostID != "host-1" || mapped.AgentID != "cf9a357f-869f-4606-b2e8-6bc4da69b32c" ||
		mapped.Status != "idle" || mapped.Worktree != "spike-44494:1" {
		t.Fatalf("mapped detail = %#v", mapped)
	}
}
