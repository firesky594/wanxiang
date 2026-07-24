package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCheckUsesAllowlistWorktreeAndRedactsOutput(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module checkdemo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=workspace-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".docker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".docker", "config.json"), []byte(`{"auths":{"registry":{"auth":"workspace-secret"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "credentials.yaml"), []byte("token: workspace-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Secrets.JSON"), []byte(`{"token":"workspace-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "env"), []byte("TOKEN=workspace-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.env"), []byte("TOKEN=workspace-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".secrets", "value"), []byte("workspace-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main_test.go"), []byte(`package src
import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)
func TestSecret(t *testing.T) {
	fmt.Println("API_KEY=hidden-value")
	for _, path := range []string{"/workspace/.env", "/workspace/env", "/workspace/config.env", "/workspace/.secrets/value", "/workspace/.docker/config.json", "/workspace/credentials.yaml", "/workspace/Secrets.JSON", "/workspace-source/.env", "/workspace-source/.docker/config.json"} {
		if _, err := os.ReadFile(path); !os.IsNotExist(err) {
			t.Fatalf("sensitive sandbox path %s is readable: %v", path, err)
		}
	}
	for _, path := range []string{"/root-write", "/dev/dev-write", "/proc/proc-write"} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err == nil {
			t.Fatalf("unbounded sandbox path %s is writable", path)
		}
	}
	var workspace syscall.Statfs_t
	if err := syscall.Statfs(".", &workspace); err != nil {
		t.Fatal(err)
	}
	if total := uint64(workspace.Blocks) * uint64(workspace.Bsize); total > 128*1024*1024 {
		t.Fatalf("workspace quota=%d", total)
	}
	var temporary syscall.Statfs_t
	if err := syscall.Statfs("/tmp", &temporary); err != nil {
		t.Fatal(err)
	}
	if total := uint64(temporary.Blocks) * uint64(temporary.Bsize); total > 512*1024*1024 {
		t.Fatalf("temporary quota=%d", total)
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(status), "CapEff:\t0000000000000000") {
		t.Fatalf("capabilities not dropped: %s", status)
	}
	limits, err := os.ReadFile("/proc/self/limits")
	if err != nil {
		t.Fatal(err)
	}
	processLimit := false
	for _, line := range strings.Split(string(limits), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "Max" && fields[1] == "processes" && fields[2] == "128" && fields[3] == "128" {
			processLimit = true
			break
		}
	}
	if !processLimit {
		t.Fatalf("process limit not applied: %s", limits)
	}
	if err := os.Mkdir("quota-probe", 0o700); err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), 1024*1024)
	hitLimit := false
	for index := 0; index < 256; index++ {
		if err := os.WriteFile(fmt.Sprintf("quota-probe/%03d", index), chunk, 0o600); err != nil {
			hitLimit = true
			break
		}
	}
	if !hitLimit {
		t.Fatal("workspace total quota was not enforced")
	}
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := NewCheckRunner(tools.db, tools.leases)
	result := runner.RunCheck(t.Context(), ref, CheckRequest{Command: "go", Args: []string{"test", "-v", "./src"}, Timeout: 20 * time.Second})
	if result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("result=%+v", result)
	}
	if strings.Contains(result.Output, "hidden-value") || !strings.Contains(result.Output, "[REDACTED]") {
		t.Fatalf("output=%q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "quota-probe")); !os.IsNotExist(err) {
		t.Fatalf("sandbox writes escaped to controlled workspace: %v", err)
	}
}

func TestRunCheckRejectsShellDangerousAndInvalidLease(t *testing.T) {
	tools, ref, _ := fileToolsFixture(t)
	runner := NewCheckRunner(tools.db, tools.leases)
	requests := []CheckRequest{
		{Command: "bash", Args: []string{"-c", "go test ./..."}},
		{Command: "go", Args: []string{"test", "./...", "|", "cat"}},
		{Command: "go", Args: []string{"test", "./...", ">out"}},
		{Command: "rm", Args: []string{"-rf", "."}},
		{Command: "go", Args: []string{"run", "./cmd/deploy"}},
		{Command: "npm", Args: []string{"run", "deploy"}},
	}
	for _, request := range requests {
		if got := runner.RunCheck(t.Context(), ref, request); got.Error == "" {
			t.Fatalf("accepted %+v", request)
		}
	}
	bad := ref
	bad.LeaseVersion++
	if got := runner.RunCheck(t.Context(), bad, CheckRequest{Command: "go", Args: []string{"test", "./..."}}); got.Error == "" {
		t.Fatal("invalid lease accepted")
	}
}

func TestRunCheckEnforcesTimeoutAndOutputLimit(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module timeoutdemo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := NewCheckRunner(tools.db, tools.leases)
	result := runner.RunCheck(t.Context(), ref, CheckRequest{Command: "go", Args: []string{"test", "./..."}, Timeout: time.Nanosecond})
	if !result.TimedOut {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Output) > maxCheckOutputBytes {
		t.Fatalf("output length=%d", len(result.Output))
	}
}

func TestPackageScriptRejectsShellBackgrounding(t *testing.T) {
	for _, script := range []string{
		"vitest & fallocate -l 1G /tmp/fill",
		"vitest&fallocate -l 1G /tmp/fill",
		"vitest && eslint . & webpack",
	} {
		if safePackageScript(script) {
			t.Fatalf("unsafe package script accepted: %q", script)
		}
	}
	if !safePackageScript("vitest && eslint .") {
		t.Fatal("safe package script rejected")
	}
}

func TestSandboxCommandAppliesResourceLimits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module sandboxlimits\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	command, err := sandboxedCheckCommand(t.Context(), root, binary, []string{"test", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	for _, expected := range []string{"/usr/bin/prlimit", "--nproc=128:128", "--as=4294967296:4294967296", "--fsize=134217728:134217728", "--cpu=120:120", "--tmpfs /workspace", "size=134217728", "nr_inodes=32768", "/usr/bin/tar", "/usr/bin/umount /workspace-source", "remount,ro,nosuid,nodev,noexec /", "remount,ro,nosuid,noexec /dev", "/usr/bin/setpriv"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("sandbox command lacks %q: %s", expected, joined)
		}
	}
}
