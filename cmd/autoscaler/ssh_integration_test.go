package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// skipWithoutSSH skips if ssh exe.dev isn't reachable with the default config.
func skipWithoutSSH(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "ConnectTimeout=5", "-n", "exe.dev", "ls", "--json")
	if err := cmd.Run(); err != nil {
		t.Skipf("skipping: ssh exe.dev not reachable: %v", err)
	}
}

func TestRealSSHExecutorListVMs(t *testing.T) {
	skipWithoutSSH(t)
	e := &RealSSHExecutor{}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vms, err := e.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	// ListVMs may return zero VMs if the autoscaler cleaned up everything.
	// Just verify we got a valid (possibly empty) result without errors.
	for _, vm := range vms {
		if vm.VMName == "" {
			t.Error("VMInfo.VMName is empty")
		}
	}
	t.Logf("RealSSHExecutor.ListVMs: %d VMs", len(vms))
}

const realTestVMName = "exeunt-sshtest"

func TestRealSSHExecutorVMLifecycle(t *testing.T) {
	skipWithoutSSH(t)
	e := &RealSSHExecutor{}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Clean up any leftover test VM first
	vms, _ := e.ListVMs(ctx)
	for _, vm := range vms {
		if vm.VMName == realTestVMName {
			t.Logf("pre-cleanup: removing leftover %s", realTestVMName)
			e.RemoveVM(ctx, realTestVMName)
			time.Sleep(2 * time.Second)
		}
	}

	// Always clean up
	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		e.RemoveVM(cleanCtx, realTestVMName)
	}()

	// NewVM
	t.Log("NewVM...")
	if err := e.NewVM(ctx, realTestVMName, ""); err != nil {
		t.Fatalf("NewVM: %v", err)
	}

	// Verify it appears in list
	vms, err := e.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	found := false
	for _, vm := range vms {
		if vm.VMName == realTestVMName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("VM %s not in list after creation", realTestVMName)
	}

	// WaitForSSH
	t.Log("WaitForSSH...")
	if err := e.WaitForSSH(ctx, realTestVMName); err != nil {
		t.Fatalf("WaitForSSH: %v", err)
	}

	// RunOnVM
	t.Log("RunOnVM...")
	out, err := e.RunOnVM(ctx, realTestVMName, "echo hello-ssh-test && hostname")
	if err != nil {
		t.Fatalf("RunOnVM: %v", err)
	}
	if !strings.Contains(out, "hello-ssh-test") {
		t.Errorf("RunOnVM output = %q, want 'hello-ssh-test'", out)
	}
	t.Logf("RunOnVM: %s", strings.TrimSpace(out))

	// RemoveVM
	t.Log("RemoveVM...")
	if err := e.RemoveVM(ctx, realTestVMName); err != nil {
		t.Fatalf("RemoveVM: %v", err)
	}

	// Verify it's gone
	time.Sleep(2 * time.Second)
	vms, _ = e.ListVMs(ctx)
	for _, vm := range vms {
		if vm.VMName == realTestVMName {
			t.Errorf("VM %s still in list after removal", realTestVMName)
		}
	}
}
