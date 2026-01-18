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
- Integrates with RouterOS API
- Uses DHCP + ARP tables
- Performs auto-discovery of ASIC devices
- Derives online/offline state at network level
- Publishes network-related events to NATS

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

## 3. Fixed Ports and Endpoints (IMPORTANT)

### NATS JetStream (Windows / Docker / on-prem)

| Purpose | Host Port | Container Port | Notes |
|------|----------|----------------|------|
| NATS client protocol | 14222 | 4222 | NATS protocol only (no HTTP) |
| NATS monitoring (HTTP) | 18222 | 8222 | `/varz`, `/jsz`, `/connz` |

**Canonical NATS start command:**
```powershell
docker run -d `
  --name nats `
  -p 14222:4222 `
  -p 18222:8222 `
  nats:2.10 `
  -js `
  -m 8222
```

---

## 4. First run (v0 UI)

### Core (HTTP + UI)

```powershell
cd .\asic-control
# Use your .env or set env vars manually
go run .\cmd\core
```

Open UI: `http://127.0.0.1:8080/`

### MikroTik discovery service

```powershell
cd .\asic-control
go run .\cmd\mikrotik
```

When events flow, the UI updates in realtime (SSE) and `/api/devices` reflects current state.

---

## 5. Portability (copy to another PC)

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