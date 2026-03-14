---
description: Debug a stuck or failed GitHub Actions runner job. Pass a run URL, job ID, or repo name to investigate.
---

Debug a GitHub Actions runner scheduling issue. The user may provide a run URL, job ID, or just a repo name.

## Investigation steps

1. **Identify the stuck/failed job**
   - If given a URL, extract the run ID and job ID
   - Use `gh api repos/{owner}/{repo}/actions/runs/{run_id}/jobs` to get job details
   - Look for jobs with status "queued" that have been waiting too long, or "failure" status

2. **Check runner status in GitHub**
   - `gh api repos/{owner}/{repo}/actions/runners` — list registered runners
   - Look for offline runners, stale registrations (runners that should have been cleaned up)
   - Note runner names matching `exeunt-*` pattern

3. **Check the autoscaler**
   - `ssh exeunt.exe.xyz "systemctl status exeunt-autoscaler"` — is it running?
   - `ssh exeunt.exe.xyz "journalctl -u exeunt-autoscaler --since '30 min ago' --no-pager"` — recent logs
   - Filter logs for the repo: `grep '{repo_name}'`
   - Look for ERROR and WARN entries, especially:
     - `all backends failed` — no backend could provision
     - `409` errors — stale GitHub runner registrations
     - `Runner did not connect` — runner process failed to start
     - `provisioning failed` — backend-specific failures

4. **Check the autoscaler tracker state**
   - `ssh exeunt.exe.xyz "cat /var/lib/exeunt-autoscaler/state.json"` — current tracked VMs
   - Look for entries stuck in "ready" or "provisioning" status for too long (>5 min)
   - Cross-reference with actual backend state

5. **Check backend resources**
   - Docker (boxloader): `ssh boxloader "docker ps -a --filter name=exeunt --format '{{.Names}}\t{{.Status}}'"` — list containers
   - exe.dev: `ssh exe.dev ls --json 2>&1 | jq '[.vms[] | select(.name | startswith("exeunt-"))]'`
   - Compare container/VM count with tracker entries — orphans indicate leaks

6. **Check webhook delivery**
   - `gh api repos/{owner}/{repo}/hooks` — find webhook ID
   - `gh api repos/{owner}/{repo}/hooks/{id}/deliveries` — check recent deliveries
   - Look for non-200 status codes or missing "queued" events

7. **Inspect runner logs if container exists**
   - `ssh boxloader "docker exec {container} cat /home/exedev/actions-runner/_diag/Runner_*.log 2>&1 | tail -50"`
   - `ssh boxloader "docker exec {container} ps aux"` — is the runner process still running?
   - Look for SocketException, IOException, or "Operation canceled" errors

## Common failure patterns

- **Job stealing**: Runner provisioned for job B picks up older queued job A. Look for: completed webhook for a job that isn't in the tracker ("job not tracked, ignoring completed event").
- **409 name collision**: Stale GitHub runner registration from failed provisioning. Fix: delete the offline runner via `gh api -X DELETE repos/{owner}/{repo}/actions/runners/{id}`.
- **Orphaned containers**: Containers exist but aren't in the tracker. Fix: `ssh boxloader "docker rm -f {name}"`.
- **Stale tracker entries**: Entries stuck in "ready" while the runner process has exited. The container runs `sleep infinity` so it stays alive even after the runner exits.

## Report findings

Summarize:
- What failed and why (root cause)
- What state needs manual cleanup (stale runners, orphaned containers, stuck tracker entries)
- Whether the autoscaler code has a gap that needs fixing
