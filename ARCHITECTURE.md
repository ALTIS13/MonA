# ASIC Control Platform — Architecture

## 1. Overview

This project is an on-prem, event-driven platform for monitoring and controlling ASIC miners
(Antminer, Whatsminer, Avalon, IceRiver, etc.) at scale (3k+ devices).

Core principles:
- Event-driven architecture
- NATS JetStream as the single message bus
- Pull-based consumers with backpressure
- Clear separation of responsibilities
- UI does NOT connect to NATS directly

---

## 2. High-Level Components

### Core
- Central aggregation and orchestration service
- Consumes events from NATS
- Maintains current device state (in-memory for now)
- Exposes HTTP API for UI
- Publishes commands/events to NATS

### MikroTik Service
- Optional (build-tagged) integration with RouterOS API
- Kept for future: DHCP/ARP driven discovery and network-level health
- Not required for the single-binary core workflow

### Collector
- Stateless worker service
- Pull-based consumers from NATS
- Communicates directly with ASIC devices
- Executes polling and control commands
- Publishes poll results back to NATS

### UI
- Web-based frontend
- Displays device state and events
- Initiates actions via Core HTTP API
- Receives realtime updates via SSE/WebSocket
- NEVER connects to NATS directly

---

## 3. Ports and Endpoints (IMPORTANT)

### Core HTTP

- Default: `:8080`
- If busy: auto-fallback to `:8081..` and saves to `data/settings.json`

### Embedded NATS JetStream (default)

Enabled by default and configurable in UI → **Settings**:

- NATS client: default `127.0.0.1:14222`
- NATS monitoring: default `127.0.0.1:18222`

### External NATS (optional)

You can disable embedded NATS and point `NATS URL` to an external server in **Settings**.

---

## 4. Discovery model (btcTools-like)

Discovery is driven by **address pools**, configured in UI → **Discovery**:

- Supports **CIDR** (`10.10.0.0/16`)
- Supports **manual ranges** (`10.10.1.10-10.10.1.200`)
- Supports **multiple segments** (comma/newline separated)
- Pools have **notes** (rack/vlan/room) and **enabled** toggle

Main UI → **Devices** provides:

- **Scan discovery**: scan all enabled pools
- **Stop scans**: stop all running scans
- Global progress + per-pool progress

Device enrichment (best-effort):
- **Stock Antminer**: `/cgi-bin/*` JSON endpoints using **Basic + Digest** auth
- **Vnish/Anthill**: UI often serves SPA HTML; MonA detects `AnthillOS` via HTML meta and then tries `/api/*` JSON endpoints (cookie/session)

---

## 5. Settings and runtime state

Runtime state is stored in `data/`:

- `data/settings.json` — settings + saved pools
- `data/nats/` — embedded JetStream storage (if enabled)

These files are runtime-only (not committed).

---

## 6. First run (Windows / PowerShell)

```powershell
cd .\asic-control
go run .\cmd\core
```

Open UI at the printed address (e.g. `http://127.0.0.1:8080/`).

To stop correctly (free ports): UI → **Settings** → **Exit**.

---

## 7. Portability (copy to another PC)

If you want to move the project folder to another machine and build/run with minimal network dependency:

```powershell
cd .\asic-control
go mod tidy
go mod vendor
```

Then copy the whole `asic-control` folder. On the target machine:

```powershell
cd .\asic-control
go run -mod=vendor .\cmd\core
```

NOTE: MikroTik module is optional and currently disabled in default builds.