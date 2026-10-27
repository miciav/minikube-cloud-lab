// Package serviceexposure publishes managed Kubernetes services for local students.
package serviceexposure

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// Forward describes one TCP listener and its managed node target.
type Forward struct {
	ListenPort int32  `json:"listenPort"`
	Target     string `json:"target"`
}

// RunProxy forwards raw TCP until cancellation; it never interprets HTTP or TLS.
func RunProxy(parent context.Context, forwards []Forward) error {
	if parent.Err() != nil {
		return nil
	}
	if err := validateForwards(forwards); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	type binding struct {
		net.Listener
		target string
	}
	listeners := make([]binding, 0, len(forwards))
	closeListeners := func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}
	defer closeListeners()
	for _, f := range forwards {
		l, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(f.ListenPort))))
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("listen on %d: %w", f.ListenPort, err)
		}
		listeners = append(listeners, binding{l, f.Target})
	}
	var accepts, connections sync.WaitGroup
	failures := make(chan error, len(listeners))
	for _, l := range listeners {
		target := l.target
		accepts.Add(1)
		go func() {
			defer accepts.Done()
			for {
				c, err := l.Accept()
				if err != nil {
					failures <- err
					return
				}
				connections.Add(1)
				go func() { defer connections.Done(); relayTCP(ctx, c, target) }()
			}
		}()
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-failures:
	}
	cancel()
	closeListeners()
	accepts.Wait()
	connections.Wait()
	if parent.Err() != nil {
		return nil
	}
	return err
}

func validateForwards(forwards []Forward) error {
	if len(forwards) == 0 {
		return fmt.Errorf("at least one TCP forward is required")
	}
	seen := make(map[int32]bool, len(forwards))
	for _, f := range forwards {
		if f.ListenPort < 1 || f.ListenPort > 65535 || seen[f.ListenPort] {
			return fmt.Errorf("invalid or duplicate TCP listener port %d", f.ListenPort)
		}
		host, port, err := net.SplitHostPort(f.Target)
		value, parseErr := strconv.Atoi(port)
		if err != nil || parseErr != nil || host == "" || value < 1 || value > 65535 {
			return fmt.Errorf("invalid TCP target %q", f.Target)
		}
		seen[f.ListenPort] = true
	}
	return nil
}

func relayTCP(ctx context.Context, incoming net.Conn, target string) {
	defer func() { _ = incoming.Close() }()
	stopIncoming := context.AfterFunc(ctx, func() { _ = incoming.Close() })
	defer stopIncoming()
	outgoing, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", target)
	if err != nil {
		return
	}
	defer func() { _ = outgoing.Close() }()
	stopOutgoing := context.AfterFunc(ctx, func() { _ = outgoing.Close() })
	defer stopOutgoing()
	sent := make(chan struct{})
	go func() {
		_, _ = io.Copy(outgoing, incoming)
		_ = outgoing.(*net.TCPConn).CloseWrite()
		close(sent)
	}()
	_, _ = io.Copy(incoming, outgoing)
	_ = incoming.(*net.TCPConn).CloseWrite()
	<-sent
}
