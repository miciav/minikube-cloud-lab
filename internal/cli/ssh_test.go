package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func authorizedLine(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// startSSHServer runs an in-process server: "exit 3" exits 3, other commands echo,
// and a shell echoes each stdin line until "exit".
func startSSHServer(t *testing.T) (cloud.SSHAccess, *atomic.Int32) {
	t.Helper()
	_, hostSecret, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostSecret)
	clientPublic, clientSecret, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(clientSecret, "")
	clientKey, _ := ssh.NewPublicKey(clientPublic)
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() == "docker" && bytes.Equal(key.Marshal(), clientKey.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	config.AddHostKey(hostSigner)
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	sessions := &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSSH(conn, config, sessions)
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return cloud.SSHAccess{User: "docker", Host: host, Port: number, PrivateKey: string(pem.EncodeToMemory(block)), HostKey: authorizedLine(hostSigner.PublicKey())}, sessions
}

func serveSSH(conn net.Conn, config *ssh.ServerConfig, sessions *atomic.Int32) {
	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	for incoming := range channels {
		channel, reqs, err := incoming.Accept()
		if err != nil {
			continue
		}
		sessions.Add(1)
		go func() {
			defer func() { _ = channel.Close() }()
			exit := func(status uint32) {
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			}
			for req := range reqs {
				switch req.Type {
				case "exec":
					var payload struct{ Command string }
					_ = ssh.Unmarshal(req.Payload, &payload)
					_ = req.Reply(true, nil)
					if payload.Command == "exit 3" {
						exit(3)
						return
					}
					_, _ = fmt.Fprintf(channel, "ran: %s", payload.Command)
					exit(0)
					return
				case "shell":
					_ = req.Reply(true, nil)
					lines := bufio.NewScanner(channel)
					for lines.Scan() && lines.Text() != "exit" {
						_, _ = fmt.Fprintf(channel, "shell: %s\n", lines.Text())
					}
					exit(0)
					return
				default:
					_ = req.Reply(false, nil)
				}
			}
		}()
	}
}

func TestRunSSHCommand(t *testing.T) {
	access, _ := startSSHServer(t)
	var out bytes.Buffer
	if err := runSSH(context.Background(), access, []string{"sudo", "docker", "ps"}, nil, &out, io.Discard); err != nil || out.String() != "ran: sudo docker ps" {
		t.Fatalf("command: %q %v", out.String(), err)
	}
}

func TestRunSSHExitStatus(t *testing.T) {
	access, _ := startSSHServer(t)
	err := runSSH(context.Background(), access, []string{"exit", "3"}, nil, io.Discard, io.Discard)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("exit status: %v", err)
	}
}

func TestRunSSHShellWithoutTTY(t *testing.T) {
	access, _ := startSSHServer(t)
	var out bytes.Buffer
	if err := runSSH(context.Background(), access, nil, strings.NewReader("echo hi\nexit\n"), &out, io.Discard); err != nil || !strings.Contains(out.String(), "shell: echo hi") {
		t.Fatalf("shell: %q %v", out.String(), err)
	}
}

func TestRunSSHRejectsWrongHostKey(t *testing.T) {
	access, sessions := startSSHServer(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(other)
	access.HostKey = authorizedLine(signer.PublicKey())
	err := runSSH(context.Background(), access, []string{"true"}, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "host key") || sessions.Load() != 0 {
		t.Fatalf("wrong host key: %v (sessions %d)", err, sessions.Load())
	}
}

func TestSSHCommandArguments(t *testing.T) {
	_, url := setup(t, nil)
	if _, err := execute(t, url, "instances", "ssh"); err == nil {
		t.Fatal("accepted a missing name")
	}
	if _, err := execute(t, url, "instances", "ssh", "vm1", "ls"); err == nil || !strings.Contains(err.Error(), "use -- before the remote command") {
		t.Fatalf("command without --: %v", err)
	}
}

// sshAPI serves only the SSH credentials endpoint, pointing at the in-process server.
func sshAPI(t *testing.T, access cloud.SSHAccess) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/instances/vm1/ssh", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(access) // #nosec G117 -- serves a throwaway key generated by this test.
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func TestSSHCommandRunsThroughAPI(t *testing.T) {
	access, _ := startSSHServer(t)
	url := sshAPI(t, access)
	if out, err := execute(t, url, "instances", "ssh", "vm1", "--", "uname", "-a"); err != nil || out != "ran: uname -a" {
		t.Fatalf("ssh: %q %v", out, err)
	}
	_, err := execute(t, url, "instances", "ssh", "vm1", "--", "exit", "3")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 || err.Error() != "remote command exited with status 3" {
		t.Fatalf("exit status: %v", err)
	}
}

func TestRunSSHRejectsBadCredentials(t *testing.T) {
	access, _ := startSSHServer(t)
	for name, mutate := range map[string]func(*cloud.SSHAccess){
		"instance key":      func(a *cloud.SSHAccess) { a.PrivateKey = "garbage" },
		"instance host key": func(a *cloud.SSHAccess) { a.HostKey = "garbage" },
		"connect":           func(a *cloud.SSHAccess) { a.Port = 1 },
	} {
		bad := access
		mutate(&bad)
		if err := runSSH(context.Background(), bad, []string{"true"}, nil, io.Discard, io.Discard); err == nil || (name != "connect" && !strings.Contains(err.Error(), name)) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
