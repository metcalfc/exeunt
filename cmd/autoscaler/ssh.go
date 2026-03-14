package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type SSHExecutor interface {
	NewVM(ctx context.Context, name, image string) error
	RemoveVM(ctx context.Context, name string) error
	ListVMs(ctx context.Context) ([]VMInfo, error)
	WaitForSSH(ctx context.Context, name string) error
	RunOnVM(ctx context.Context, name, script string) (string, error)
}

type VMInfo struct {
	VMName string `json:"vm_name"`
	Status string `json:"status"`
}

type VMListResponse struct {
	VMs []VMInfo `json:"vms"`
}

type RealSSHExecutor struct{}

func (e *RealSSHExecutor) NewVM(ctx context.Context, name, image string) error {
	cmd := exec.CommandContext(ctx, "ssh", "-n", "exe.dev",
		"new", "--name="+name, "--image="+image, "--json", "--no-email")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh exe.dev new: %w: %s", err, stderr.String())
	}
	return nil
}

func (e *RealSSHExecutor) RemoveVM(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "ssh", "-n", "exe.dev", "rm", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh exe.dev rm %s: %w: %s", name, err, stderr.String())
	}
	return nil
}

func (e *RealSSHExecutor) ListVMs(ctx context.Context) ([]VMInfo, error) {
	cmd := exec.CommandContext(ctx, "ssh", "-n", "exe.dev", "ls", "--json")
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

func (e *RealSSHExecutor) WaitForSSH(ctx context.Context, name string) error {
	host := name + ".exe.xyz"
	for i := 0; i < 15; i++ {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(checkCtx, "ssh",
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

func (e *RealSSHExecutor) RunOnVM(ctx context.Context, name, script string) (string, error) {
	host := name + ".exe.xyz"
	cmd := exec.CommandContext(ctx, "ssh", host, "bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s: %w: %s", host, err, stderr.String())
	}
	return stdout.String(), nil
}
