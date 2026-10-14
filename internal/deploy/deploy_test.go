package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"appforce/internal/agentbundle"
	"appforce/internal/model"
	"golang.org/x/crypto/ssh"
)

type sshFixture struct {
	listener    net.Listener
	fingerprint string
	mu          sync.Mutex
	commands    []string
	inputs      []string
	root        bool
	osName      string
	archName    string
	failInstall bool
	conns       []net.Conn
	wg          sync.WaitGroup
}

func newSSHFixture(t *testing.T, root bool) *sshFixture {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &sshFixture{listener: l, root: root, fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), osName: "Linux", archName: "x86_64"}
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if c.User() != "deployer" || string(password) != "ssh-secret" {
			return nil, fmt.Errorf("invalid login")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, conn)
			f.mu.Unlock()
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				defer conn.Close()
				_, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				for ch := range channels {
					if ch.ChannelType() != "session" {
						_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := ch.Accept()
					if err != nil {
						return
					}
					f.wg.Add(1)
					go func() {
						defer f.wg.Done()
						defer channel.Close()
						for request := range requests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							var body struct{ Command string }
							_ = ssh.Unmarshal(request.Payload, &body)
							_ = request.Reply(true, nil)
							input, _ := io.ReadAll(channel)
							f.mu.Lock()
							f.commands = append(f.commands, body.Command)
							f.inputs = append(f.inputs, string(input))
							f.mu.Unlock()
							output, status := f.execute(body.Command)
							_, _ = io.WriteString(channel, output)
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
							return
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		f.mu.Lock()
		for _, conn := range f.conns {
			_ = conn.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *sshFixture) execute(command string) (string, uint32) {
	switch {
	case command == "uname -s; uname -m; hostname; id -u":
		uid := "1000"
		if f.root {
			uid = "0"
		}
		return f.osName + "\n" + f.archName + "\nfixture-host\n" + uid + "\n", 0
	case strings.Contains(command, "mktemp -d"):
		return "/tmp/appforce-deploy.fixture\n", 0
	case strings.Contains(command, "APPFORCE_SERVICE"):
		if f.failInstall {
			return "service start failed ssh-secret sudo-secret", 1
		}
		return "installed", 0
	case strings.Contains(command, "--pairing-info"):
		return `{"token":"` + strings.Repeat("a", 64) + `","fingerprint":"` + strings.Repeat("b", 64) + `"}`, 0
	default:
		return "", 0
	}
}

func (f *sshFixture) request() Request {
	port := f.listener.Addr().(*net.TCPAddr).Port
	return Request{Name: "fixture", Host: "127.0.0.1", Port: port, Username: "deployer", Password: "ssh-secret", SudoPassword: "sudo-secret", ManagementPort: 17891, HostFingerprint: f.fingerprint}
}

func fixtureManager(t *testing.T) (*Manager, *model.Device) {
	t.Helper()
	var saved model.Device
	m := NewManager(func(device model.Device) (string, error) { saved = device; return "saved-id", nil })
	payload := "fixture executable payload"
	hash := sha256.Sum256([]byte(payload))
	m.openBundle = func(goos, arch string) (io.ReadCloser, agentbundle.Platform, error) {
		if (goos != "linux" && goos != "darwin") || (arch != "amd64" && arch != "arm64") {
			t.Errorf("unexpected platform %s/%s", goos, arch)
		}
		return io.NopCloser(strings.NewReader(payload)), agentbundle.Platform{OS: goos, Arch: arch, Size: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}, nil
	}
	m.ready = func(_ context.Context, device model.Device) error {
		if device.Token != strings.Repeat("a", 64) || device.CertificateSHA256 != strings.Repeat("b", 64) {
			t.Errorf("wrong generated credentials")
		}
		return nil
	}
	return m, &saved
}

func awaitJob(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := m.Job(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != "running" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("deployment did not complete")
	return Job{}
}

func TestSSHDeploymentUploadsVerifiedAgentAndUsesSudoStdin(t *testing.T) {
	f := newSSHFixture(t, false)
	r := f.request()
	check, err := Inspect(context.Background(), r)
	if err != nil || check.OS != "linux" || check.Arch != "amd64" || check.Root || check.HostFingerprint != f.fingerprint {
		t.Fatalf("inspection: %+v %v", check, err)
	}
	m, saved := fixtureManager(t)
	job, err := m.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	finished := awaitJob(t, m, job.ID)
	if finished.Status != "succeeded" || finished.DeviceID != "saved-id" || saved.URL != "https://127.0.0.1:17891" {
		t.Fatalf("deployment failed: %+v %+v", finished, saved)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	uploaded, sudo := false, false
	for i, command := range f.commands {
		if strings.Contains(command, r.Password) || strings.Contains(command, r.SudoPassword) {
			t.Fatal("password exposed in shell command")
		}
		if strings.Contains(command, "cat >") && !strings.Contains(command, "APPFORCE_SERVICE") {
			uploaded = f.inputs[i] == "fixture executable payload"
		}
		if strings.HasPrefix(command, "sudo ") {
			sudo = true
			if f.inputs[i] != "sudo-secret\n" {
				t.Fatal("sudo password not delivered on stdin")
			}
		}
	}
	if !uploaded || !sudo {
		t.Fatalf("missing upload or elevation: %v %v", uploaded, sudo)
	}
}

func TestChangedHostKeyWrongPasswordAndDeploymentErrorsAreSafe(t *testing.T) {
	f := newSSHFixture(t, true)
	r := f.request()
	r.HostFingerprint = "SHA256:another-host"
	if _, err := Inspect(context.Background(), r); err == nil || !strings.Contains(err.Error(), "指纹") {
		t.Fatalf("host mismatch accepted: %v", err)
	}
	r = f.request()
	r.Password = "wrong-password"
	if _, err := Inspect(context.Background(), r); err == nil || strings.Contains(err.Error(), r.Password) {
		t.Fatalf("bad login accepted or leaked: %v", err)
	}
	r = f.request()
	f.failInstall = true
	m, saved := fixtureManager(t)
	job, err := m.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	finished := awaitJob(t, m, job.ID)
	if finished.Status != "failed" || saved.URL != "" || strings.Contains(finished.Error, r.Password) || strings.Contains(finished.Error, r.SudoPassword) {
		t.Fatalf("failure leaked credentials or saved device: %+v", finished)
	}
}

func TestShellQuotingAndServiceInstallRollback(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		cmd := exec.Command("/bin/sh", "-n")
		cmd.Stdin = strings.NewReader(installation(goos, "/tmp/appforce-deploy.ab'cd", strings.Repeat("a", 64), 17891))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("invalid %s install script: %s %v", goos, output, err)
		}
	}
	malicious := "a'\n$(echo wrong); `echo wrong`"
	output, err := exec.Command("/bin/sh", "-c", "printf %s "+quote(malicious)).Output()
	if err != nil || string(output) != malicious {
		t.Fatal("shell quoting changed literal input")
	}
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			root := t.TempDir()
			temp := filepath.Join(root, "upload")
			_ = os.MkdirAll(temp, 0700)
			payload := []byte("#!/bin/sh\nexit 0\n")
			_ = os.WriteFile(filepath.Join(temp, "appforce"), payload, 0700)
			hash := sha256.Sum256(payload)
			binary, data, service := paths("linux")
			sandboxBinary, sandboxData, sandboxService := root+binary, root+data, root+service
			_ = os.MkdirAll(filepath.Dir(sandboxBinary), 0755)
			_ = os.MkdirAll(filepath.Dir(sandboxService), 0755)
			_ = os.WriteFile(sandboxBinary, []byte("previous binary"), 0755)
			_ = os.WriteFile(sandboxService, []byte("previous service"), 0644)
			toolsDir := filepath.Join(root, "tools")
			_ = os.MkdirAll(toolsDir, 0755)
			_ = os.WriteFile(filepath.Join(toolsDir, "chown"), []byte("#!/bin/sh\nexit 0\n"), 0755)
			status := "0"
			if fail {
				status = "1"
			}
			_ = os.WriteFile(filepath.Join(toolsDir, "systemctl"), []byte("#!/bin/sh\nif [ \"$1\" = start ]; then exit "+status+"; fi\nexit 0\n"), 0755)
			if _, err := exec.LookPath("sha256sum"); err != nil {
				_ = os.WriteFile(filepath.Join(toolsDir, "sha256sum"), []byte("#!/bin/sh\nshasum -a 256 \"$@\"\n"), 0755)
			}
			script := installation("linux", temp, hex.EncodeToString(hash[:]), 17891)
			script = strings.NewReplacer(binary, sandboxBinary, dir(binary), dir(sandboxBinary), data, sandboxData, service, sandboxService).Replace(script)
			cmd := exec.Command("/bin/sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+toolsDir+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if fail && err == nil || !fail && err != nil {
				t.Fatalf("installation status: %s %v", output, err)
			}
			current, _ := os.ReadFile(sandboxBinary)
			unit, _ := os.ReadFile(sandboxService)
			if fail && (string(current) != "previous binary" || string(unit) != "previous service") {
				t.Fatal("failed update destroyed prior install")
			}
			if !fail && (string(current) != string(payload) || !strings.Contains(string(unit), "--tls-cert")) {
				t.Fatal("successful install missing binary or HTTPS service")
			}
		})
	}
}

func TestHealthRequiresMatchingCertificateTokenAndAgentState(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			w.WriteHeader(401)
			return
		}
		_, _ = fmt.Fprintf(w, `{"version":%q,"os":"linux"}`, model.Version)
	}))
	defer server.Close()
	hash := sha256.Sum256(server.Certificate().Raw)
	device := model.Device{URL: server.URL, Token: strings.Repeat("a", 64), CertificateSHA256: hex.EncodeToString(hash[:])}
	if err := waitReady(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	device.CertificateSHA256 = strings.Repeat("0", 64)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := waitReady(ctx, device); err == nil {
		t.Fatal("accepted wrong certificate")
	}
}

func TestRequestValidation(t *testing.T) {
	valid := Request{Name: "test", Host: "[::1]", Username: "user", Password: "secret"}
	if err := valid.Validate(); err != nil || valid.Port != 22 || valid.ManagementPort != 17891 || valid.Host != "::1" {
		t.Fatalf("defaults or IPv6: %+v %v", valid, err)
	}
	for _, host := range []string{"http://host", "host:22", "host\ncommand", "host/user", "host user"} {
		r := valid
		r.Host = host
		if err := r.Validate(); err == nil {
			t.Fatalf("accepted invalid host %q", host)
		}
	}
	if managementURL("::1", 17891) != "https://[::1]:17891" {
		t.Fatal("IPv6 URL is invalid")
	}
}

func TestMacDeploymentSelectsAppleSiliconAgentAndLaunchDaemon(t *testing.T) {
	f := newSSHFixture(t, false)
	f.osName, f.archName = "Darwin", "arm64"
	m, _ := fixtureManager(t)
	baseOpen := m.openBundle
	chosen := false
	m.openBundle = func(goos, arch string) (io.ReadCloser, agentbundle.Platform, error) {
		chosen = goos == "darwin" && arch == "arm64"
		return baseOpen(goos, arch)
	}
	job, err := m.Start(f.request())
	if err != nil {
		t.Fatal(err)
	}
	if finished := awaitJob(t, m, job.ID); finished.Status != "succeeded" || !chosen {
		t.Fatalf("Mac deploy failed: %+v", finished)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	installed := false
	for _, command := range f.commands {
		if strings.Contains(command, "APPFORCE_SERVICE") {
			installed = strings.Contains(command, "launchctl bootstrap") && strings.Contains(command, "/Library/LaunchDaemons/com.appforce.agent.plist") && strings.Contains(command, "codesign --force --sign -") && strings.Contains(command, "--verify-agent")
		}
	}
	if !installed {
		t.Fatal("Mac launchd service was not installed")
	}
}

func TestRootDeployDoesNotInvokeSudo(t *testing.T) {
	f := newSSHFixture(t, true)
	m, _ := fixtureManager(t)
	job, err := m.Start(f.request())
	if err != nil {
		t.Fatal(err)
	}
	if finished := awaitJob(t, m, job.ID); finished.Status != "succeeded" {
		t.Fatal(finished.Error)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, command := range f.commands {
		if strings.HasPrefix(command, "sudo ") {
			t.Fatal("root was asked for sudo password")
		}
	}
}

func TestShutdownRejectsNewDeployment(t *testing.T) {
	m, _ := fixtureManager(t)
	m.Close()
	r := Request{Name: "test", Host: "127.0.0.1", Username: "test", Password: "secret", HostFingerprint: "SHA256:fixture"}
	if _, err := m.Start(r); err == nil {
		t.Fatal("deployment accepted while shutting down")
	}
}
