package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"mini-sre/internal/config"
)

func TestLocalCommands(t *testing.T) {
	r := &Runner{}
	result, err := r.Run(context.Background(), "local", "printf", "%s", "a ' $HOME; echo nope")
	if err != nil || result.Stdout != "a ' $HOME; echo nope" {
		t.Fatalf("literal arguments: %+v, %v", result, err)
	}
	result, err = r.Run(context.Background(), "local", "sh", "-c", "printf failure >&2; exit 3")
	if err == nil || result.Stderr != "failure" {
		t.Fatalf("exit failure: %+v, %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.Run(ctx, "local", "sleep", "10"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel command: %v", err)
	}
	if _, err := r.Run(context.Background(), "missing", "true"); err == nil {
		t.Fatal("unknown host accepted")
	}
}

func TestOutputLimitAndRedaction(t *testing.T) {
	r := &Runner{Config: config.Config{Hosts: map[string]config.Host{"web": {Password: "secret-password"}}}}
	result, err := r.Run(context.Background(), "local", "sh", "-c", "printf secret-password >&2; head -c 100000 /dev/zero")
	if err != nil || !result.Truncated || len(result.Stdout)+len(result.Stderr) > outputLimit {
		t.Fatalf("output limit: sizes %d/%d, truncated=%v, err=%v", len(result.Stdout), len(result.Stderr), result.Truncated, err)
	}
	if strings.Contains(r.Redact(result.Stderr), "secret-password") {
		t.Fatal("credential leaked")
	}
}

func TestSSHCommands(t *testing.T) {
	for _, useKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("key=%v", useKey), func(t *testing.T) {
			r := testSSH(t, useKey)
			for _, args := range [][]string{
				{"%s", "a ' $HOME; echo nope\nsecond line"},
				{"%s", ""},
			} {
				result, err := r.Run(context.Background(), "web", "printf", args...)
				if err != nil || result.Stdout != args[1] {
					t.Fatalf("SSH literal arguments: %+v, %v", result, err)
				}
			}
			result, err := r.Run(context.Background(), "web", "sh", "-c", "printf failure >&2; exit 7")
			if err == nil || result.Stderr != "failure" {
				t.Fatalf("SSH exit failure: %+v, %v", result, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, err := r.Run(ctx, "web", "sleep", "10"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("SSH cancel command: %v", err)
			}
			if err := os.WriteFile(r.Config.KnownHosts, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Run(context.Background(), "web", "true"); err == nil || !strings.Contains(err.Error(), "host key") {
				t.Fatalf("unknown host key: %v", err)
			}
		})
	}
}

func TestSSHHandshakeCancellation(t *testing.T) {
	r := testSSH(t, false)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn)
	}()
	h := r.Config.Hosts["web"]
	h.Port = l.Addr().(*net.TCPAddr).Port
	r.Config.Hosts["web"] = h
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	if _, err := r.Run(ctx, "web", "true"); !errors.Is(err, context.Canceled) {
		t.Fatalf("handshake cancellation: %v", err)
	}
	<-done
}

func testSSH(t *testing.T, useKey bool) *Runner {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) != "secret-password" {
				return nil, errors.New("bad password")
			}
			return nil, nil
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(signer.PublicKey().Marshal()) {
				return nil, errors.New("bad key")
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() { serveSSH(conn, serverConfig) })
		}
	})
	t.Cleanup(func() { l.Close(); wg.Wait() })
	dir := t.TempDir()
	knownFile := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{l.Addr().String()}, signer.PublicKey()) + "\n"
	if err := os.WriteFile(knownFile, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	host := config.Host{Address: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, User: "sre", Password: "secret-password"}
	if useKey {
		block, err := ssh.MarshalPrivateKey(private, "test")
		if err != nil {
			t.Fatal(err)
		}
		host.Password = ""
		host.KeyFile = filepath.Join(dir, "key")
		if err := os.WriteFile(host.KeyFile, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &Runner{Config: config.Config{KnownHosts: knownFile, Hosts: map[string]config.Host{"web": host}}}
}

func serveSSH(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	server, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		server.Wait()
		cancel()
	}()
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, reqs, err := newChannel.Accept()
		if err != nil {
			return
		}
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			defer channel.Close()
			for req := range reqs {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				if ssh.Unmarshal(req.Payload, &payload) != nil {
					req.Reply(false, nil)
					return
				}
				req.Reply(true, nil)
				cmd := exec.CommandContext(ctx, "sh", "-c", "exec env "+payload.Command)
				cmd.Stdout, cmd.Stderr = channel, channel.Stderr()
				cmd.WaitDelay = time.Second
				err := cmd.Run()
				status := 0
				if err != nil {
					status = 1
					var exit *exec.ExitError
					if errors.As(err, &exit) {
						status = exit.ExitCode()
					}
				}
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
				return
			}
		}()
		<-finished
	}
}

func TestSSHAuthenticationFailure(t *testing.T) {
	r := testSSH(t, false)
	h := r.Config.Hosts["web"]
	h.Password = "wrong-secret"
	r.Config.Hosts["web"] = h
	_, err := r.Run(context.Background(), "web", "true")
	if err == nil || !strings.Contains(err.Error(), "authentication") || strings.Contains(err.Error(), h.Password) {
		t.Fatalf("authentication failure: %v", err)
	}
	if got := r.Hosts(); strings.Join(got, ",") != "local,web" {
		t.Fatal(got)
	}
}
