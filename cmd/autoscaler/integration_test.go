package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testSSHExecutor is a real SSH executor that uses the den-exe-test keypair.
// Every method makes real SSH calls to exe.dev — this is not a mock.
type testSSHExecutor struct {
	identityFile string
}

func (e *testSSHExecutor) sshCmd(ctx context.Context, args ...string) *exec.Cmd {
	fullArgs := []string{"-i", e.identityFile, "-o", "StrictHostKeyChecking=accept-new", "-n"}
	fullArgs = append(fullArgs, args...)
	return exec.CommandContext(ctx, "ssh", fullArgs...)
}

func (e *testSSHExecutor) NewVM(ctx context.Context, name, image string) error {
	args := []string{"exe.dev", "new", "--name=" + name, "--json", "--no-email"}
	if image != "" {
		args = append(args, "--image="+image)
	}
	cmd := e.sshCmd(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh exe.dev new: %w: %s", err, stderr.String())
	}
	return nil
}

func (e *testSSHExecutor) RemoveVM(ctx context.Context, name string) error {
	cmd := e.sshCmd(ctx, "exe.dev", "rm", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh exe.dev rm %s: %w: %s", name, err, stderr.String())
	}
	return nil
}

func (e *testSSHExecutor) ListVMs(ctx context.Context) ([]VMInfo, error) {
	cmd := e.sshCmd(ctx, "exe.dev", "ls", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh exe.dev ls: %w: %s", err, stderr.String())
	}
	var resp VMListResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse vm list: %w", err)
	}
	return resp.VMs, nil
}

func (e *testSSHExecutor) WaitForSSH(ctx context.Context, name string) error {
	host := name + ".exe.xyz"
	for i := 0; i < 15; i++ {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(checkCtx, "ssh",
			"-i", e.identityFile,
			"-o", "ConnectTimeout=5",
			"-o", "StrictHostKeyChecking=accept-new",
			host, "true")
		err := cmd.Run()
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("VM %s not SSH-accessible after 15 attempts", name)
}

func (e *testSSHExecutor) RunOnVM(ctx context.Context, name, script string) (string, error) {
	host := name + ".exe.xyz"
	cmd := exec.CommandContext(ctx, "ssh",
		"-i", e.identityFile,
		"-o", "StrictHostKeyChecking=accept-new",
		host, "bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s: %w: %s", host, err, stderr.String())
	}
	return stdout.String(), nil
}

func skipWithoutExeDev(t *testing.T) *testSSHExecutor {
	t.Helper()
	keyFile := os.Getenv("EXE_TEST_SSH_KEY")
	if keyFile == "" {
		keyFile = filepath.Join(os.Getenv("HOME"), ".ssh", "den-exe-test")
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Skipf("skipping exe.dev integration test: key %s not found", keyFile)
	}
	// Quick connectivity check
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-i", keyFile, "-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=5", "-n", "exe.dev", "ls", "--json")
	if err := cmd.Run(); err != nil {
		t.Skipf("skipping exe.dev integration test: connectivity check failed: %v", err)
	}
	return &testSSHExecutor{identityFile: keyFile}
}

const testVMName = "exeunt-inttest"

func TestIntegrationExeDevListVMs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ssh := skipWithoutExeDev(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vms, err := ssh.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}

	// Should have at least one VM (the pre-existing ones)
	if len(vms) == 0 {
		t.Fatal("expected at least one VM from ListVMs")
	}

	// Verify VMInfo fields are populated
	for _, vm := range vms {
		if vm.VMName == "" {
			t.Error("VMInfo.VMName is empty")
		}
		if vm.Status == "" {
			t.Error("VMInfo.Status is empty")
		}
	}

	t.Logf("found %d VMs", len(vms))
}

func TestIntegrationExeDevBackendListRunners(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ssh := skipWithoutExeDev(t)
	logger := newTestLogger()

	backend := NewExeDevBackend(BackendConfig{
		Name:       "integration-test",
		Type:       "exedev",
		MaxRunners: 5,
		Labels:     []string{"exe"},
		Priority:   1,
	}, "test:latest", ssh, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runners, err := backend.ListRunners(ctx)
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}

	// Should return runner names (we know there are running VMs)
	if len(runners) == 0 {
		t.Fatal("expected at least one runner from ListRunners")
	}

	// All runner names should be non-empty
	for _, name := range runners {
		if name == "" {
			t.Error("runner name is empty")
		}
	}

	t.Logf("found %d runners: %v", len(runners), runners)
}

func TestIntegrationExeDevVMLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ssh := skipWithoutExeDev(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Check if test VM already exists — if so, remove it first
	vms, err := ssh.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	for _, vm := range vms {
		if vm.VMName == testVMName {
			t.Logf("test VM %s already exists, removing", testVMName)
			if err := ssh.RemoveVM(ctx, testVMName); err != nil {
				t.Fatalf("pre-cleanup RemoveVM: %v", err)
			}
			time.Sleep(2 * time.Second)
		}
	}

	// Create VM
	t.Log("creating test VM...")
	if err := ssh.NewVM(ctx, testVMName, ""); err != nil {
		t.Fatalf("NewVM: %v", err)
	}

	// Always clean up the test VM, even if test fails
	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		t.Log("cleaning up test VM...")
		if err := ssh.RemoveVM(cleanCtx, testVMName); err != nil {
			t.Logf("cleanup RemoveVM: %v (may need manual cleanup)", err)
		}
	}()

	// Verify it appears in the list
	vms, err = ssh.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs after create: %v", err)
	}
	found := false
	for _, vm := range vms {
		if vm.VMName == testVMName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("test VM %s not found in ListVMs after creation", testVMName)
	}

	// Wait for SSH
	t.Log("waiting for SSH...")
	if err := ssh.WaitForSSH(ctx, testVMName); err != nil {
		t.Fatalf("WaitForSSH: %v", err)
	}

	// Run a command
	t.Log("running command on VM...")
	out, err := ssh.RunOnVM(ctx, testVMName, "echo hello-from-test && hostname")
	if err != nil {
		t.Fatalf("RunOnVM: %v", err)
	}
	if !strings.Contains(out, "hello-from-test") {
		t.Errorf("RunOnVM output = %q, expected 'hello-from-test'", out)
	}
	t.Logf("RunOnVM output: %s", strings.TrimSpace(out))

	// Remove VM (will also be called by defer)
	t.Log("destroying test VM...")
	if err := ssh.RemoveVM(ctx, testVMName); err != nil {
		t.Fatalf("RemoveVM: %v", err)
	}

	// Verify it's gone
	time.Sleep(2 * time.Second)
	vms, err = ssh.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs after remove: %v", err)
	}
	for _, vm := range vms {
		if vm.VMName == testVMName {
			t.Errorf("test VM %s still in list after removal", testVMName)
		}
	}
}

func TestIntegrationFullProvisionerFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ssh := skipWithoutExeDev(t)
	logger := newTestLogger()

	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")

	cfg := &Config{
		RunnerImage: "",
		Repos:       []string{"metcalfc/exeunt"},
		Backends: []BackendConfig{
			{Name: "test-backend", Type: "exedev", MaxRunners: 2, Labels: []string{"exe"}, Priority: 1},
		},
		StateFile: stateFile,
	}

	tracker := NewTracker(stateFile, logger)
	backend := NewExeDevBackend(cfg.Backends[0], cfg.RunnerImage, ssh, logger)
	router := NewRouter([]Backend{backend}, tracker, logger)

	// Test provisioner with real backend: Create + Destroy lifecycle
	// We skip GenerateJITConfig (needs real GitHub token) and StartRunner
	// (needs runner binary), but we test CreateRunner + DestroyRunner.

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	vmName := "exeunt-flowtest"

	// Clean up any pre-existing test VM
	vms, _ := ssh.ListVMs(ctx)
	for _, vm := range vms {
		if vm.VMName == vmName {
			ssh.RemoveVM(ctx, vmName)
			time.Sleep(2 * time.Second)
		}
	}

	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		ssh.RemoveVM(cleanCtx, vmName)
	}()

	// Create runner via backend
	t.Log("CreateRunner...")
	if err := backend.CreateRunner(ctx, vmName, cfg.RunnerImage); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	// Track it
	tracker.Add(1, vmName, "metcalfc/exeunt", "test-backend", []string{"exe"})

	// Verify tracker state
	if tracker.Count() != 1 {
		t.Fatalf("tracker count = %d, want 1", tracker.Count())
	}
	record, ok := tracker.Get(1)
	if !ok {
		t.Fatal("expected job 1 in tracker")
	}
	if record.VMName != vmName {
		t.Errorf("VMName = %q, want %q", record.VMName, vmName)
	}

	// Verify VM appears in ListRunners
	runners, err := backend.ListRunners(ctx)
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}
	foundRunner := false
	for _, name := range runners {
		if name == vmName {
			foundRunner = true
		}
	}
	if !foundRunner {
		t.Errorf("VM %s not found in ListRunners: %v", vmName, runners)
	}

	// Reconcile should keep the VM (it exists)
	reconcile(ctx, tracker, []Backend{backend}, make(chan struct{}, 10), logger)
	if !tracker.HasJob(1) {
		t.Error("reconcile removed job 1 but VM still exists")
	}

	// Destroy via backend
	t.Log("DestroyRunner...")
	if err := backend.DestroyRunner(ctx, vmName); err != nil {
		t.Fatalf("DestroyRunner: %v", err)
	}
	tracker.Remove(1)

	// Verify tracker is empty
	if tracker.Count() != 0 {
		t.Errorf("tracker count = %d after destroy, want 0", tracker.Count())
	}

	// Verify state file was persisted
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var records []json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("parse state file: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("state file has %d records after cleanup, want 0", len(records))
	}

	// Router should find the backend
	b := router.BackendByName("test-backend")
	if b == nil {
		t.Fatal("BackendByName returned nil")
	}
	if router.TotalCapacity() != 2 {
		t.Errorf("TotalCapacity = %d, want 2", router.TotalCapacity())
	}
}
