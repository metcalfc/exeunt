---
description: Proactive health check of the autoscaler infrastructure. Finds orphaned containers, stale state, webhook issues, and resource leaks before they cause failures.
---

Run a comprehensive health check of the exeunt autoscaler infrastructure. This catches problems that don't show up as GitHub CI failures but will cause failures later.

## Managed repos

The autoscaler manages runners for these repos (check deploy/config.local.json for current list):
- metcalfc/exeunt
- metcalfc/exeuntu
- abrihq/den

## Checks to perform

Run ALL of these checks and report a summary at the end.

### 1. Autoscaler process health
- `ssh exeunt.exe.xyz "systemctl status exeunt-autoscaler --no-pager"` — must be `active (running)`
- `ssh exeunt.exe.xyz "journalctl -u exeunt-autoscaler --since '1 hour ago' --no-pager"` — scan for ERROR and WARN lines
- Count errors in last hour — more than 5 is a red flag

### 2. Tracker vs backend drift
- Read tracker state: `ssh exeunt.exe.xyz "cat /var/lib/exeunt-autoscaler/state.json"`
- List actual containers: `ssh boxloader "docker ps --filter name=exeunt --format '{{.Names}}\t{{.Status}}'"`
- **Orphaned containers**: containers on boxloader not in tracker — these leak resources
- **Ghost tracker entries**: tracker entries whose containers no longer exist — these block semaphore slots
- **Stale entries**: tracker entries older than 10 minutes in "ready" or "provisioning" status — the runner process likely exited

### 3. Stale GitHub runner registrations
For each managed repo:
- `gh api repos/{owner}/{repo}/actions/runners --jq '.runners[] | select(.name | startswith("exeunt-")) | {id, name, status}'`
- **Offline runners**: registrations for runners that no longer exist — these cause 409 errors and job stealing
- Delete any found: `gh api -X DELETE repos/{owner}/{repo}/actions/runners/{id}`

### 4. Webhook health
For each managed repo:
- `gh api repos/{owner}/{repo}/hooks --jq '.[] | select(.config.url | contains("exeunt")) | {id, active, config: {url: .config.url}}'`
- Verify webhook URL is `https://exeunt.exe.xyz/webhook` and active is true
- Check recent deliveries: `gh api repos/{owner}/{repo}/hooks/{id}/deliveries --jq '.[:5][] | {status_code, action, delivered_at}'`
- Flag any non-200 status codes in last 5 deliveries

### 5. Boxloader Docker host health
- `ssh boxloader "docker info --format '{{.ContainersRunning}} running / {{.Images}} images'"` — basic health
- `ssh boxloader "df -h / --output=pcent"` — disk space (>80% is a warning)
- `ssh boxloader "docker system df"` — docker disk usage, flag if reclaimable space is large

### 6. Stuck GitHub Actions jobs
For each managed repo:
- `gh api repos/{owner}/{repo}/actions/runs --jq '.workflow_runs[] | select(.status == "queued" or .status == "in_progress") | {id, name, status, created_at}'`
- Flag any jobs queued for more than 5 minutes
- Flag any jobs in_progress for more than 30 minutes

### 7. exe.dev VM state
- `ssh exe.dev ls --json 2>&1` — list VMs
- Verify `exeunt.exe.xyz` exists and is running (the autoscaler host)
- Flag any unexpected `exeunt-*` VMs (should be empty if exe.dev backend is disabled)

## Remediation

For each issue found, take the fix action directly:
- Delete stale GitHub runners
- Remove orphaned containers (`ssh boxloader "docker rm -f {name}"`)
- Log warnings for issues that need human attention (disk space, process crashes, webhook failures)

Do NOT restart the autoscaler service unless it's actually down.

## Output format

```
=== Exeunt Health Check ===

Autoscaler:     OK | DEGRADED | DOWN
Tracker:        OK | {n} orphans, {n} ghosts, {n} stale
GitHub Runners: OK | {n} stale registrations (cleaned)
Webhooks:       OK | {issues}
Boxloader:      OK | {issues}
Queued Jobs:    OK | {n} stuck
exe.dev:        OK | {issues}

Actions taken:
- {list of fixes applied}

Issues needing attention:
- {list of things that need human intervention}
```
