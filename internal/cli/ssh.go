package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

// ExitError carries a remote command's exit status to the process exit code.
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

func sshCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh NAME [-- COMMAND...]",
		Short: "Open an SSH session to a running instance",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("requires an instance name")
			}
			if len(args) > 1 && cmd.ArgsLenAtDash() != 1 {
				return errors.New("use -- before the remote command")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			access, err := client(cmd).InstanceSSH(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return runSSH(cmd.Context(), access, args[1:], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// runSSH runs command, or a shell when it is empty, verifying the recorded host key.
func runSSH(ctx context.Context, access cloud.SSHAccess, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	signer, err := ssh.ParsePrivateKey([]byte(access.PrivateKey))
	if err != nil {
		return fmt.Errorf("instance key: %w", err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(access.HostKey))
	if err != nil {
		return fmt.Errorf("instance host key: %w", err)
	}
	address := net.JoinHostPort(access.Host, strconv.Itoa(access.Port))
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	// Ask for the recorded key's type; sshd also offers others that would fail the fixed check.
	config := &ssh.ClientConfig{User: access.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(hostKey), HostKeyAlgorithms: []string{hostKey.Type()}, Timeout: 10 * time.Second}
	c, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("connect to %s: %w", address, err)
	}
	client := ssh.NewClient(c, channels, requests)
	defer func() { _ = client.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()
	session.Stdin, session.Stdout, session.Stderr = stdin, stdout, stderr
	if len(command) > 0 {
		err = session.Run(strings.Join(command, " ")) // OpenSSH joins arguments the same way.
	} else {
		err = shell(session, stdin)
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return &ExitError{Code: exit.ExitStatus()}
	}
	return err
}

// shell attaches a PTY only when stdin is a terminal; pipes get a plain shell.
func shell(session *ssh.Session, stdin io.Reader) error {
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fd := int(file.Fd())
		width, height, err := term.GetSize(fd)
		if err != nil {
			width, height = 80, 24
		}
		kind := os.Getenv("TERM")
		if kind == "" {
			kind = "xterm-256color"
		}
		if err := session.RequestPty(kind, height, width, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			return err
		}
		state, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer func() { _ = term.Restore(fd, state) }()
		resize := make(chan os.Signal, 1)
		signal.Notify(resize, syscall.SIGWINCH)
		defer func() { signal.Stop(resize); close(resize) }()
		go func() {
			for range resize {
				if w, h, err := term.GetSize(fd); err == nil {
					_ = session.WindowChange(h, w)
				}
			}
		}()
	}
	if err := session.Shell(); err != nil {
		return err
	}
	return session.Wait()
}
