package cli

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestServiceProxyCommand(t *testing.T) {
	for _, tc := range []struct{ name, config, want string }{
		{"bad-json", "{", "decode proxy config"},
		{"empty", "[]", "at least one"},
		{"bad-port", `[{"listenPort":0,"target":"127.0.0.1:80"}]`, "listener port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRoot()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"service-proxy", "--config-json", tc.config})
			err := cmd.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("proxy command did not validate config: %v", err)
			}
		})
	}
	cmd := NewRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"service-proxy", "--config-json", `[{"listenPort":30080,"target":"127.0.0.1:80"}]`})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("normal proxy cancellation: %v", err)
	}
}
