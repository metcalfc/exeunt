# Operations

Day-to-day management of the autoscaler and runner infrastructure.

## Makefile targets

```bash
make deploy                  # build + push binary, config, systemd unit, restart
make restart                 # rebuild binary, hot-swap, keep existing config
make start                   # start the service
make stop                    # stop the service
make status                  # systemd status
make logs                    # last 50 journal lines
make test                    # run unit tests
make clean                   # remove local build artifacts
```

All targets accept `HOST=` to target a specific machine:

```bash
HOST=porcupine.exe.xyz make status
```

## Monitoring

The autoscaler exposes two HTTP endpoints on the exe.dev VM's public URL.

**Health check:**
```bash
curl https://porcupine.exe.xyz/healthz
```
```json
{"status":"ok","uptime":"2h15m","active_vms":1,"backends":2}
```

**Detailed status** (lists every active runner):
```bash
curl https://porcupine.exe.xyz/status
```

**Logs** are structured JSON written to stdout, captured by the systemd journal:

```bash
# Recent logs
make logs

# Follow live
ssh porcupine.exe.xyz sudo journalctl -u exeunt-autoscaler -f

# Just errors
ssh porcupine.exe.xyz sudo journalctl -u exeunt-autoscaler --no-pager | grep ERROR
```

## Upgrading

```bash
git pull
make deploy    # full deploy: binary + config + systemd unit
# or
make restart   # just the binary, keep existing config on the VM
```

## Adding a server

1. Install Docker and Tailscale on the machine
2. Run `tailscale up --ssh`
3. Tag it as `tag:exework` in [Tailscale admin](https://login.tailscale.com/admin/machines)
4. Pull the runner image: `docker pull ghcr.io/metcalfc/exeunt-runner:latest`
5. Set up the hourly pull cron (see [setup.md](setup.md#1-prepare-your-server))
6. Add a backend entry to `deploy/config.local.json`
7. `make deploy`

The autoscaler picks up the new backend immediately on restart.

## Adding a repo

1. Add a webhook to the repo pointing at the autoscaler (see [setup.md](setup.md#7-github-webhook))
2. Add the repo (or a glob like `your-org/*`) to the `repos` list in `deploy/config.local.json`
3. `make deploy`
4. Make sure the `AUTOSCALER_GITHUB_TOKEN` PAT has access to the repo

## Runner image

The runner image is built from a [fork of exeuntu](https://github.com/metcalfc/exeuntu) (the exe.dev base image) with these additions:

- GitHub Actions runner binary (pre-installed, avoids 15-20s download at boot)
- Toolchains via mise: Go, Python, Ruby, Rust, Node
- CLI tools: buf, ruff, golangci-lint, actionlint, lefthook, markdownlint-cli, goimports, govulncheck
- Tailscale (disabled by default, activated per-VM or per-container)
- Claude Code and Codex
- Docker, git, and standard Ubuntu dev tooling

Toolchains are split across Docker layers so that bumping a CLI tool version doesn't trigger a Ruby recompile:

- **Tier 1** (separate layers, expensive, rarely change): Rust, Ruby, Go, Python, Node
- **Tier 2** (single layer, cheap): CLI tools from `.mise.toml`

The image build runs on the persistent `exe-builder` runner, so Docker layer cache persists between builds. A registry cache at GHCR backs it up.

## Automated workflows

| Workflow | Schedule | Purpose |
|----------|----------|---------|
| `build-runner-image` | Daily 06:00 UTC | Builds the runner image with the latest GitHub Actions runner version and pushes to GHCR. Skips if the version hasn't changed. |
| `sync-exeuntu-fork` | Daily 05:00 UTC | Rebases our fork patches onto upstream exeuntu. Opens an issue if the rebase fails. |
| `reaper` | Every 15 min | Cross-references exe.dev VMs against GitHub runners and destroys orphaned `exeunt-*` VMs. |

## Reaper

The reaper is a safety net. It runs every 15 minutes and checks each `exeunt-*` VM on exe.dev against the list of registered GitHub runners. If a VM has no corresponding runner (or the runner is offline), the reaper destroys it.

This catches:
- VMs leaked by failed teardowns
- VMs from cancelled workflows
- VMs left behind if the autoscaler crashes during provisioning

The autoscaler VM itself is named outside the `exeunt-*` pattern (`porcupine` in our examples), so the reaper leaves it alone.

## Tailscale key rotation

If you used a Tailscale auth key (not OAuth) when setting up the autoscaler VM, it expires after 90 days. To rotate:

```bash
ssh porcupine.exe.xyz sudo tailscale up --authkey=NEW_KEY --hostname=porcupine --ssh
```

Generate a new key at [Tailscale admin > Keys](https://login.tailscale.com/admin/settings/keys). To avoid this entirely, use an OAuth client — those don't expire.

The Tailscale OIDC credentials for GitHub Actions runners don't expire and don't need rotation.
