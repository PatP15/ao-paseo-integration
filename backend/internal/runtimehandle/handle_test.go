package runtimehandle

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNamespacedHandleRoundTrip(t *testing.T) {
	handle, err := New(domain.ExecutionBackendPaseo, "worker-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID != "paseo:worker-1/agent-1" {
		t.Fatalf("handle = %q", handle.ID)
	}
	parts, found, err := Parse(handle)
	if err != nil || !found {
		t.Fatalf("parse found=%v err=%v", found, err)
	}
	if parts.Backend != domain.ExecutionBackendPaseo || parts.HostID != "worker-1" || parts.AgentID != "agent-1" {
		t.Fatalf("parts = %#v", parts)
	}
}

func TestParseKeepsLocalHandlesOpaqueAndRejectsMalformedRemoteHandles(t *testing.T) {
	if _, found, err := Parse(ports.RuntimeHandle{ID: "local-handle"}); err != nil || found {
		t.Fatalf("local parse found=%v err=%v", found, err)
	}
	for _, id := range []string{"paseo:", "paseo:host", "paseo:/agent", "paseo:host/", "paseo:host/agent/extra", "paseo:host:other/agent"} {
		if _, found, err := Parse(ports.RuntimeHandle{ID: id}); !found || err == nil {
			t.Fatalf("malformed %q found=%v err=%v", id, found, err)
		}
		if !IsNamespaced(id) {
			t.Fatalf("malformed prefixed handle %q was not namespaced", id)
		}
	}
}

func TestNewRejectsReservedDelimiters(t *testing.T) {
	for _, test := range []struct {
		backend domain.ExecutionBackendType
		host    domain.ExecutionHostID
		agent   domain.ExecutionAgentID
	}{
		{"", "host", "agent"},
		{domain.ExecutionBackendPaseo, "host/other", "agent"},
		{domain.ExecutionBackendPaseo, "host", "agent:other"},
	} {
		if _, err := New(test.backend, test.host, test.agent); err == nil {
			t.Fatalf("New(%q, %q, %q) succeeded", test.backend, test.host, test.agent)
		}
	}
}
