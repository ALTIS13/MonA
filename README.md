## MonA (asic-control)

On-prem platform for ASIC discovery/monitoring/control (btcTools-like), optimized for large fleets.

### What already works (v0.1.0)

- **Single binary / single program** (no `.env` required)
- **Embedded NATS JetStream** (optional; enabled by default)
- **Web UI** served by `core` (embedded static UI)
- **Discovery**:
  - address pools (CIDR or manual ranges, multiple per line)
  - notes per pool
  - enable/disable pools
  - scan all pools from main screen
  - per-pool progress + global progress
- **Scanner enrichment (no creds)**:
  - TCP ports probe
  - cgminer API probe (4028): worker, uptime, hashrate (best-effort)
  - model/vendor normalization (best-effort)
- **Credentials (stored, encrypted)**:
  - managed in UI
  - stored encrypted in `data/settings.json` using `data/secret.key`
- **Antminer deep probe (stock)**:
  - `/cgi-bin/summary.cgi` + `/cgi-bin/stats.cgi`
  - supports **Basic** and **Digest** auth (lighttpd)
- **Vnish/Anthill support**:
  - devices are detected from the web UI HTML meta (`AnthillOS`)
  - enrichment uses best-effort `/api/*` probing (cookie/session login) and extracts model/hashrate/uptime/fans/temps when JSON API exists
- **Clean shutdown**: Exit button frees ports and stops embedded NATS/scans

### Run (Windows / PowerShell)

```powershell
cd .\asic-control
go run .\cmd\core
```

Open the UI at the printed address (auto picks `:8080..:8100` if busy).

### Data directory

Runtime state is stored in `data/`:

- `data/settings.json` — app settings and saved address pools
- `data/nats/` — embedded JetStream storage (if enabled)

These files are **not committed** (see `.gitignore`).

