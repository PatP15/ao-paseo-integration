package paseo

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeCommandsKeepHostOnSubcommand(t *testing.T) {
	t.Parallel()
	logs, err := logsArgs("worker:6767", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	send, err := sendArgs("worker:6767", "agent-1", "continue")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"logs", "--host", "worker:6767", "agent-1"}; !reflect.DeepEqual(logs, want) {
		t.Fatalf("logs args = %#v, want %#v", logs, want)
	}
	if want := []string{"send", "--host", "worker:6767", "agent-1", "continue"}; !reflect.DeepEqual(send, want) {
		t.Fatalf("send args = %#v, want %#v", send, want)
	}
}

func TestLogsRedactsSecretsFromRenderedTranscript(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{results: []commandResult{{stdout: []byte("password=hunter2 https://app.paseo.sh/#offer=abc_DEF")}}}
	client := &Client{host: "tcp://worker:6767?password=hunter2", version: SupportedVersion, runner: run}
	got, err := client.Logs(context.Background(), "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "abc_DEF") {
		t.Fatalf("secret leaked: %s", got)
	}
}
