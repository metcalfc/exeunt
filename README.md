# exeunt

<p align="center">
  <img src="exeunt-pufferfish.png" alt="Three pufferfish heading for the exit" width="600">
</p>

*"Exeunt all."* — stage direction for when everyone leaves.

Ephemeral [exe.dev](https://exe.dev) runners for GitHub Actions. VMs spin up, do the work, and exit. Hence the name.

(Not to be confused with [exeuntu](https://github.com/boldsoftware/exeuntu), the exe.dev base image. Yes, we noticed.)

## What this is

A pair of composite actions that provision and tear down exe.dev VMs as GitHub Actions self-hosted runners. Each VM is a full Ubuntu 24.04 environment with Docker, git, and dev tooling pre-installed — no containers-in-containers, no Docker-in-Docker hacks.

## Quick start

```yaml
jobs:
  provision:
    runs-on: ubuntu-latest
    outputs:
      vm_name: ${{ steps.setup.outputs.vm_name }}
    steps:
      - uses: actions/checkout@v4
      - uses: metcalfc/exeunt/setup-exe-runner@main
        id: setup
        with:
          exe-ssh-key: ${{ secrets.EXE_SSH_KEY }}
          github-token: ${{ secrets.GH_RUNNER_TOKEN }}

  work:
    needs: provision
    runs-on: [self-hosted, exe]
    steps:
      - uses: actions/checkout@v4
      - run: echo "Running on a real VM"

  teardown:
    runs-on: ubuntu-latest
    needs: [provision, work]
    if: always()
    steps:
      - uses: actions/checkout@v4
      - uses: metcalfc/exeunt/teardown-exe-runner@main
        with:
          exe-ssh-key: ${{ secrets.EXE_SSH_KEY }}
          vm-name: ${{ needs.provision.outputs.vm_name }}
```

## Actions

### `setup-exe-runner`

Provisions an exe.dev VM and registers it as an ephemeral JIT runner.

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `exe-ssh-key` | yes | | SSH private key (ed25519) for exe.dev |
| `github-token` | yes | | Token with `administration:write` for JIT runner config |
| `labels` | no | `exe` | Comma-separated runner labels |
| `image` | no | `ghcr.io/metcalfc/exeunt-runner:latest` | Container image for the VM |

| Output | Description |
|--------|-------------|
| `vm_name` | Name of the provisioned VM |

### `teardown-exe-runner`

Destroys the VM. Run with `if: always()` so it cleans up even on failure.

| Input | Required | Description |
|-------|----------|-------------|
| `exe-ssh-key` | yes | SSH private key for exe.dev |
| `vm-name` | yes | VM name from setup output |

## Secrets

| Secret | Description |
|--------|-------------|
| `EXE_SSH_KEY` | ed25519 private key registered with exe.dev |
| `GH_RUNNER_TOKEN` | GitHub PAT with `administration:write` scope on the repo |

## Custom runner image

VMs boot from a [forked exeuntu image](https://github.com/metcalfc/exeuntu) with the GitHub Actions runner binary pre-baked, cutting ~15-20 seconds off provisioning. The image includes Claude Code, Codex, Docker, and standard dev tooling.

### Automated maintenance

| Workflow | Schedule | What it does |
|----------|----------|--------------|
| `sync-exeuntu-fork` | Daily 05:00 UTC | Rebases fork patches onto upstream, triggers build on meaningful changes |
| `build-runner-image` | Daily 06:00 UTC | Builds image with latest runner version, pushes to GHCR (skips if already current) |
| `reaper` | Every 15 min | Cross-references VMs against GitHub runners, destroys orphans |

## How it works

1. **Setup** creates a VM via `ssh exe.dev new`, generates a JIT runner config via the GitHub API, and starts the runner
2. **Your jobs** run on the VM using `runs-on: [self-hosted, exe]`
3. **Teardown** destroys the VM via `ssh exe.dev rm`
4. **Reaper** catches anything that slips through (failed teardowns, cancelled workflows)

## License

MIT
