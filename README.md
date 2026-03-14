# exeunt

<p align="center">
  <img src="exeunt-pufferfish.png" alt="Three pufferfish heading for the exit" width="600">
</p>

*"Exeunt all."* — stage direction for when everyone leaves.

Ephemeral [exe.dev](https://exe.dev) runners for GitHub Actions. VMs spin up, do the work, and exit. Hence the name.

(Not to be confused with [exeuntu](https://github.com/boldsoftware/exeuntu), the exe.dev base image. Yes, we noticed.)

## Architecture

```
GitHub webhook (workflow_job)
        │
        ▼
┌─────────────────┐
│   Autoscaler    │  (runs on exebuilder VM)
│   :8080/webhook │
└────────┬────────┘
         │ routes by label + priority
         ├──────────────────────────┐
         ▼                          ▼
┌─────────────────┐      ┌──────────────────┐
│  Docker Backend │      │  exe.dev Backend │
│  (bare metal)   │      │  (cloud VMs)     │
│  priority: 1    │      │  priority: 10    │
│  tailscale ssh  │      │  ssh exe.dev     │
└─────────────────┘      └──────────────────┘
   boxloader                exe.dev VMs
   16c/96GB                 shared 2c/8GB
```

The autoscaler receives GitHub `workflow_job` webhooks, provisions runners on the best available backend, and destroys them when jobs complete. Bare metal gets priority (faster, more resources), exe.dev is the fallback.

## Quick start

### Option 1: Autoscaler (recommended)

With the autoscaler running, any workflow just uses `runs-on`:

```yaml
jobs:
  build:
    runs-on: [self-hosted, exe]
    steps:
      - uses: actions/checkout@v5
      - run: echo "Running on a real VM or bare metal"
```

No provision/teardown jobs needed. The autoscaler handles the full lifecycle.

### Option 2: Composite actions (manual)

For repos without the autoscaler webhook, use the composite actions directly:

```yaml
jobs:
  provision:
    runs-on: ubuntu-latest
    outputs:
      vm_name: ${{ steps.setup.outputs.vm_name }}
    steps:
      - uses: actions/checkout@v5
      - uses: metcalfc/exeunt/setup-exe-runner@main
        id: setup
        with:
          exe-ssh-key: ${{ secrets.EXE_SSH_KEY }}
          github-token: ${{ secrets.GH_RUNNER_TOKEN }}

  work:
    needs: provision
    runs-on: [self-hosted, exe]
    steps:
      - uses: actions/checkout@v5
      - run: echo "Running on a real VM"

  teardown:
    runs-on: ubuntu-latest
    needs: [provision, work]
    if: always()
    steps:
      - uses: actions/checkout@v5
      - uses: metcalfc/exeunt/teardown-exe-runner@main
        with:
          exe-ssh-key: ${{ secrets.EXE_SSH_KEY }}
          vm-name: ${{ needs.provision.outputs.vm_name }}
```

## Labels

| Label | Routes to | Use case |
|-------|-----------|----------|
| `exe` | Best available (bare metal first, then exe.dev) | Default for all jobs |
| `exe-large` | Bare metal only | CPU/memory-intensive builds |
| `exe-builder` | Persistent builder VM | Image builds (Docker cache persists) |

## Setup

### Prerequisites

- [exe.dev](https://exe.dev) account with SSH key
- [Tailscale](https://tailscale.com) tailnet (for bare metal backends)
- GitHub PAT with `repo` scope (for JIT runner config)

### 1. Autoscaler VM

The autoscaler runs on a persistent exe.dev VM called `exebuilder`.

```bash
# Create the VM
ssh exe.dev new --name=exebuilder --image=ghcr.io/metcalfc/exeunt-runner:latest --no-email

# Install Tailscale and join your tailnet
ssh exebuilder.exe.xyz bash -c '
  curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg \
    | sudo tee /usr/share/keyrings/tailscale-archive-keyring.gpg > /dev/null
  curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list \
    | sudo tee /etc/apt/sources.list.d/tailscale.list > /dev/null
  sudo apt-get update && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y tailscale
  sudo systemctl enable --now tailscaled
  sudo tailscale up --authkey=YOUR_TAILSCALE_AUTH_KEY --hostname=exebuilder --ssh
'

# Register a persistent GitHub Actions runner (for image builds)
ssh exebuilder.exe.xyz 'sudo -u exedev bash -c "
  cd /home/exedev/actions-runner
  ./config.sh --url https://github.com/YOUR/REPO --token YOUR_RUNNER_TOKEN \
    --name exebuilder --labels self-hosted,exe-builder --unattended --replace
  nohup ./run.sh > runner.log 2>&1 &
"'
```

### 2. Secrets

Create `/etc/exeunt-autoscaler/env` on exebuilder:

```bash
AUTOSCALER_WEBHOOK_SECRET=<random-hex-string>
AUTOSCALER_GITHUB_TOKEN=<github-pat-with-repo-scope>
```

Generate the webhook secret: `openssl rand -hex 32`

### 3. Config

Edit `deploy/config.json` with your backends:

```json
{
  "repo": "your-org/your-repo",
  "port": 8080,
  "runner_image": "ghcr.io/metcalfc/exeunt-runner:latest",
  "backends": [
    {
      "name": "bigserver",
      "type": "docker",
      "host": "bigserver",
      "user": "youruser",
      "max_runners": 5,
      "labels": ["exe", "exe-large"],
      "priority": 1
    },
    {
      "name": "exe.dev",
      "type": "exedev",
      "max_runners": 5,
      "labels": ["exe"],
      "priority": 10
    }
  ]
}
```

### 4. Deploy

```bash
make deploy                          # deploy to exebuilder (default)
HOST=other.host make deploy          # deploy to a different host
```

### 5. GitHub webhook

Add a webhook at `https://github.com/YOUR/REPO/settings/hooks`:

- **URL:** `https://exebuilder.exe.xyz/webhook`
- **Content type:** `application/json`
- **Secret:** same value as `AUTOSCALER_WEBHOOK_SECRET`
- **Events:** select "Workflow jobs" only

Make the exe.dev proxy public: `ssh exe.dev share set-public exebuilder && ssh exe.dev share port exebuilder 8080`

### 6. Tailscale ACLs

Tag your machines and add ACL rules so the autoscaler can reach Docker hosts:

```json
"tagOwners": {
  "tag:exedev":  ["autogroup:admin"],
  "tag:exework": ["autogroup:admin"]
},
"ACLs": [
  {"Action": "accept", "Users": ["tag:exedev"], "Ports": ["tag:exework:*"]}
],
"ssh": [
  {"action": "accept", "src": ["tag:exedev"], "dst": ["tag:exework"], "users": ["autogroup:nonroot", "root"]}
]
```

- Tag exebuilder as `tag:exedev` (via Tailscale auth key)
- Tag bare metal Docker hosts as `tag:exework` (in Tailscale admin)

## Operations

### Deploy and manage

```bash
make deploy                  # build, push binary + config + systemd unit, restart
make restart                 # rebuild and hot-swap the binary
make start                   # start the service
make stop                    # stop the service
make status                  # systemd status
make logs                    # last 50 log lines
make test                    # run unit tests
HOST=other.host make status  # target a different host
```

### Monitoring

The autoscaler exposes two HTTP endpoints:

```bash
# Health check
curl https://exebuilder.exe.xyz/healthz
# {"status":"ok","uptime":"2h15m","active_vms":1,"backends":2}

# Detailed status
curl https://exebuilder.exe.xyz/status
# {"active_vms":[...],"count":1,"backends":2,"uptime":"2h15m"}
```

Logs are structured JSON to stdout, captured by systemd journal:

```bash
# Follow logs live
ssh exebuilder.exe.xyz sudo journalctl -u exeunt-autoscaler -f

# Filter by log level
ssh exebuilder.exe.xyz sudo journalctl -u exeunt-autoscaler --no-pager | grep ERROR
```

### Upgrading

```bash
# Pull latest code, rebuild, and deploy
git pull
make deploy

# Or just hot-swap the binary without updating config/systemd
make restart
```

### Adding a backend

1. Add the host to your Tailscale tailnet with `tag:exework`
2. Ensure Docker is installed on the host
3. Add an entry to `deploy/config.json`
4. `make deploy`

### Runner image

The runner image is built from a [fork of exeuntu](https://github.com/metcalfc/exeuntu) with:

- GitHub Actions runner binary pre-installed
- [mise](https://mise.jdx.dev) toolchains: Go, Python, Ruby, Rust, Node + CLI tools
- Tailscale for private networking
- Claude Code and Codex
- Docker, git, and standard dev tooling

Toolchains are split into tiered Docker layers for efficient caching:
- **Tier 1:** Heavy runtimes (Rust, Ruby, Go, Python, Node) — own layers, rarely change
- **Tier 2:** CLI tools (buf, ruff, golangci-lint, etc.) — single layer, cheap to rebuild

### Automated maintenance

| Workflow | Schedule | What it does |
|----------|----------|--------------|
| `sync-exeuntu-fork` | Daily 05:00 UTC | Rebases fork onto upstream, triggers build on changes |
| `build-runner-image` | Daily 06:00 UTC | Builds image with latest runner version, pushes to GHCR |
| `reaper` | Every 15 min | Destroys orphaned `exeunt-*` VMs (safety net) |

The image build runs on the persistent `exebuilder` VM so Docker layer cache persists between builds. Registry cache at GHCR serves as backup.

### Reaper safety net

The reaper workflow runs every 15 minutes and cross-references exe.dev VMs against registered GitHub runners. Any `exeunt-*` VM without an active runner gets destroyed. This catches:

- Failed teardowns
- Cancelled workflows
- Autoscaler crashes during provisioning

The persistent `exebuilder` VM is named outside the `exeunt-*` pattern, so the reaper ignores it.

## Composite actions

### `setup-exe-runner`

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `exe-ssh-key` | yes | | SSH private key (ed25519) for exe.dev |
| `github-token` | yes | | PAT with `repo` scope for JIT runner config |
| `labels` | no | `exe` | Comma-separated runner labels |
| `image` | no | `ghcr.io/metcalfc/exeunt-runner:latest` | Container image for the VM |

### `teardown-exe-runner`

| Input | Required | Description |
|-------|----------|-------------|
| `exe-ssh-key` | yes | SSH private key for exe.dev |
| `vm-name` | yes | VM name from setup output |

## License

MIT
