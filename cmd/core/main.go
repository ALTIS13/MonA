package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jhump/protoreflect/dynamic"
	"go.uber.org/zap"

	"asic-control/internal/bus/embeddednats"
	"asic-control/internal/bus/natsjs"
	"asic-control/internal/core/registry"
	"asic-control/internal/core/webui"
	"asic-control/internal/defaultcreds"
	"asic-control/internal/discovery/scanner"
	"asic-control/internal/discovery/subnets"
	"asic-control/internal/events"
	"asic-control/internal/logging"
	"asic-control/internal/netutil"
	"asic-control/internal/settings"
	"asic-control/internal/version"
)

func main() {
	log, err := logging.New(logging.Config{Level: "info"})
	if err != nil {
		panic(err)
	}
	defer func() { _ = log.Sync() }()

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfgStore, err := settings.Open("data")
	if err != nil {
		log.Fatal("settings open", zap.Error(err))
	}
	cfg := cfgStore.Get()

	// Embedded NATS (optional) — start before any client connections.
	var embMu sync.Mutex
	var emb *embeddednats.Server
	startEmbedded := func(s settings.Settings) {
		embMu.Lock()
		defer embMu.Unlock()
		if emb != nil {
			emb.Shutdown()
			emb = nil
		}
		if !s.EmbeddedNATS.Enabled {
			return
		}
		server, err := embeddednats.Start(embeddednats.Config{
			Host:     s.EmbeddedNATS.Host,
			Port:     s.EmbeddedNATS.Port,
			HTTPPort: s.EmbeddedNATS.HTTPPort,
			StoreDir: s.EmbeddedNATS.StoreDir,
		})
		if err != nil {
			log.Warn("embedded nats start failed", zap.Error(err))
			return
		}
		emb = server
		log.Info("embedded nats started",
			zap.String("host", s.EmbeddedNATS.Host),
			zap.Int("port", s.EmbeddedNATS.Port),
			zap.Int("http_port", s.EmbeddedNATS.HTTPPort),
		)
	}
	startEmbedded(cfg)

	schema, err := events.LoadSchema()
	if err != nil {
		log.Fatal("load proto schema", zap.Error(err))
	}

	store := registry.NewStore()
	subnetsStore := subnets.NewStore()

	// restore persisted subnets
	for _, sn := range cfg.Subnets {
		if sn.Enabled {
			_, _ = subnetsStore.AddWithNote(sn.CIDR, sn.Note)
		}
	}

	type scanJob struct {
		cancel context.CancelFunc
	}
	scanMu := sync.Mutex{}
	scans := map[int64]scanJob{}

	// NATS is optional at runtime: core must start even if NATS is down.
	var natsMu sync.RWMutex
	var natsClient *natsjs.Client
	var natsConnected atomic.Bool
	var natsLastErr atomic.Value // string

	runScan := func(scanCtx context.Context, subnetID int64, spec string) {
		defer func() {
			scanMu.Lock()
			delete(scans, subnetID)
			scanMu.Unlock()
			subnetsStore.SetScanState(subnetID, false, 100, time.Now().UTC())
		}()

		s := scanner.New(scanner.Config{
			Concurrency:      cfgStore.Get().Scanner.Concurrency,
			DialTimeout:      cfgStore.Get().Scanner.DialTimeout,
			HTTPTimeout:      cfgStore.Get().Scanner.HTTPTimeout,
			TryDefaultCreds:  cfgStore.Get().TryDefaultCreds,
		})

		_ = s.ScanSpec(scanCtx, spec,
			func(done, total int) {
				if total <= 0 {
					return
				}
				p := int(float64(done) * 100 / float64(total))
				if p < 0 {
					p = 0
				}
				if p > 100 {
					p = 100
				}
				subnetsStore.SetScanState(subnetID, true, p, time.Time{})
			},
			func(res scanner.Result) {
				ip := res.IP.String()
				now := time.Now().UTC()

				_ = store.UpsertDiscovery("scanner", ip, "", now)
				_ = store.UpsertObserved("scanner", ip, "", res.Online, now)
				store.UpdateEnrichment(ip, func(d *registry.Device) {
					d.OpenPorts = res.Open
					d.Confidence = res.Confidence
					d.Vendor = res.Vendor
					d.Model = res.Model
					d.Firmware = res.Firmware
					d.Worker = res.Worker
					d.UptimeS = res.UptimeS
					d.HashrateTHS = res.HashrateTHS
				})

				if !res.IsASIC {
					return
				}
				if !natsConnected.Load() {
					return
				}
				natsMu.RLock()
				c := natsClient
				natsMu.RUnlock()
				if c == nil {
					return
				}

				envMsg := schema.NewEnvelope(events.NetworkDeviceDiscovered)
				envMsg.SetFieldByName("shard_id", "scanner")
				envMsg.SetFieldByName("ip", ip)
				envMsg.SetFieldByName("mac", "")
				dd := dynamic.NewMessage(schema.DeviceDiscovered)
				dd.SetFieldByName("ip", ip)
				dd.SetFieldByName("mac", "")
				dd.SetFieldByName("source", "scanner.tcp")
				dd.SetFieldByName("shard_id", "scanner")
				labels := map[string]string{
					"cidr":   spec,
					"vendor": res.Vendor,
				}
				dd.SetFieldByName("labels", labels)
				envMsg.SetFieldByName("device_discovered", dd)
				if b, err := events.Marshal(envMsg); err == nil {
					_ = c.Publish(context.Background(), events.NetworkDeviceDiscovered, b)
				}

				env2 := schema.NewEnvelope(events.NetworkObserved)
				env2.SetFieldByName("shard_id", "scanner")
				env2.SetFieldByName("ip", ip)
				env2.SetFieldByName("mac", "")
				no := dynamic.NewMessage(schema.NetworkObserved)
				no.SetFieldByName("ip", ip)
				no.SetFieldByName("mac", "")
				no.SetFieldByName("online", res.Online)
				no.SetFieldByName("source", "scanner.tcp")
				facts := map[string]string{
					"open_ports": fmt.Sprintf("%v", res.Open),
				}
				no.SetFieldByName("facts", facts)
				env2.SetFieldByName("network_observed", no)
				if b, err := events.Marshal(env2); err == nil {
					_ = c.Publish(context.Background(), events.NetworkObserved, b)
				}
			},
		)
	}

	reconnectCh := make(chan struct{}, 1)
	requestReconnect := func() {
		select {
		case reconnectCh <- struct{}{}:
		default:
		}
	}

	// consumer loop (starts when connected)
	startConsumer := func(c *natsjs.Client, prefix string) {
		ctx := rootCtx
		consumer, err := c.NewPullConsumer("core-network", events.DomainNetwork+".*", 4096)
		if err != nil {
			natsLastErr.Store(err.Error())
			return
		}
		go func() {
			for natsConnected.Load() {
				select {
				case <-ctx.Done():
					return
				default:
				}
				msgs, err := consumer.Fetch(ctx, 256, 2*time.Second)
				if err != nil {
					continue
				}
				for _, m := range msgs {
					envMsg, err := events.UnmarshalEnvelope(schema, m.Data())
					if err != nil {
						_ = m.Term()
						continue
					}

					subj := envMsg.GetFieldByName("subject").(string)
					ip := envMsg.GetFieldByName("ip").(string)
					mac := envMsg.GetFieldByName("mac").(string)
					shard := envMsg.GetFieldByName("shard_id").(string)

					now := time.Now().UTC()
					switch subj {
					case events.NetworkDeviceDiscovered:
						_ = store.UpsertDiscovery(shard, ip, mac, now)
					case events.NetworkObserved:
						payload := envMsg.GetFieldByName("network_observed").(*dynamic.Message)
						online := payload.GetFieldByName("online").(bool)
						_ = store.UpsertObserved(shard, ip, mac, online, now)
					}
					_ = m.Ack()
				}
			}
		}()
	}

	// connect loop
	go func() {
		for {
			select {
			case <-rootCtx.Done():
				natsMu.Lock()
				if natsClient != nil {
					_ = natsClient.Close()
					natsClient = nil
				}
				natsMu.Unlock()
				return
			default:
			}
			cfg := cfgStore.Get()
			url := cfg.NATSURL
			prefix := cfg.NATSPrefix

			c, err := natsjs.Connect(natsjs.Config{
				URL:     url,
				Prefix:  prefix,
				Timeout: 2 * time.Second,
			})
			if err != nil {
				natsConnected.Store(false)
				natsLastErr.Store(err.Error())
				select {
				case <-rootCtx.Done():
					return
				case <-time.After(2 * time.Second):
					continue
				case <-reconnectCh:
					continue
				}
			}
			if err := c.EnsureStreams(); err != nil {
				_ = c.Close()
				natsConnected.Store(false)
				natsLastErr.Store(err.Error())
				select {
				case <-rootCtx.Done():
					return
				case <-time.After(2 * time.Second):
					continue
				case <-reconnectCh:
					continue
				}
			}

			natsMu.Lock()
			if natsClient != nil {
				_ = natsClient.Close()
			}
			natsClient = c
			natsMu.Unlock()

			natsConnected.Store(true)
			natsLastErr.Store("")
			startConsumer(c, prefix)

			// wait for explicit reconnect request
			select {
			case <-rootCtx.Done():
				natsConnected.Store(false)
				natsMu.Lock()
				if natsClient != nil {
					_ = natsClient.Close()
					natsClient = nil
				}
				natsMu.Unlock()
				return
			case <-reconnectCh:
			}
			natsConnected.Store(false)
		}
	}()

	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte(version.String()))
	})
	r.Get("/api/cidr/preview", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		cidr := r.URL.Query().Get("cidr")
		_ = json.NewEncoder(w).Encode(netutil.PreviewSpec(cidr))
	})
	r.Get("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		errStr, _ := natsLastErr.Load().(string)
		embMu.Lock()
		embOn := emb != nil
		embMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nats_connected": natsConnected.Load(),
			"nats_error":     errStr,
			"embedded_nats":  embOn,
		})
	})
	r.Get("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(store.List())
	})

	// Settings
	r.Get("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(cfgStore.Get())
	})
	r.Put("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		var s settings.Settings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		// basic normalization/defaults
		if s.Version == 0 {
			s.Version = 1
		}
		if s.HTTPAddr == "" {
			s.HTTPAddr = ":8080"
		}
		if s.NATSURL == "" {
			s.NATSURL = "nats://127.0.0.1:14222"
		}
		if s.NATSPrefix == "" {
			s.NATSPrefix = "mona"
		}
		// embedded nats defaults
		if s.EmbeddedNATS.Host == "" {
			s.EmbeddedNATS.Host = "127.0.0.1"
		}
		if s.EmbeddedNATS.Port == 0 {
			s.EmbeddedNATS.Port = 14222
		}
		if s.EmbeddedNATS.HTTPPort == 0 {
			s.EmbeddedNATS.HTTPPort = 18222
		}
		if s.EmbeddedNATS.StoreDir == "" {
			s.EmbeddedNATS.StoreDir = "data/nats"
		}
		if s.Scanner.Concurrency <= 0 {
			s.Scanner.Concurrency = 256
		}
		if s.Scanner.DialTimeout <= 0 {
			s.Scanner.DialTimeout = 600 * time.Millisecond
		}
		if s.Scanner.HTTPTimeout <= 0 {
			s.Scanner.HTTPTimeout = 1 * time.Second
		}
		// Keep TryDefaultCreds as provided (bool).
		if err := cfgStore.Update(s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Apply embedded NATS changes immediately (best-effort).
		startEmbedded(s)
		requestReconnect()
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(cfgStore.Get())
	})

	// Exit (for junior ops: "two clicks": open UI -> Settings -> Exit)
	exitCh := make(chan struct{}, 1)
	r.Post("/api/admin/exit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("bye"))
		select {
		case exitCh <- struct{}{}:
		default:
		}
	})

	// Subnets CRUD
	r.Get("/api/subnets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(subnetsStore.List())
	})
	r.Patch("/api/subnets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if id <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Enabled != nil {
			subnetsStore.SetEnabled(id, *req.Enabled)
			_ = cfgStore.Patch(func(s *settings.Settings) {
				s.Subnets = nil
				for _, x := range subnetsStore.List() {
					s.Subnets = append(s.Subnets, settings.Subnet{CIDR: x.CIDR, Enabled: x.Enabled, Note: x.Note})
				}
			})
		}
		w.WriteHeader(http.StatusAccepted)
	})

	r.Get("/api/creds/defaults", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(defaultcreds.Defaults())
	})
	r.Post("/api/subnets", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			CIDR string `json:"cidr"`
			Note string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		sub, err := subnetsStore.AddWithNote(req.CIDR, req.Note)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = cfgStore.Patch(func(s *settings.Settings) {
			s.Subnets = nil
			for _, x := range subnetsStore.List() {
				s.Subnets = append(s.Subnets, settings.Subnet{CIDR: x.CIDR, Enabled: x.Enabled, Note: x.Note})
			}
		})
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(sub)
	})
	r.Delete("/api/subnets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if id <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		// stop scan if running
		scanMu.Lock()
		if j, ok := scans[id]; ok {
			j.cancel()
			delete(scans, id)
		}
		scanMu.Unlock()

		if !subnetsStore.Delete(id) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = cfgStore.Patch(func(s *settings.Settings) {
			s.Subnets = nil
			for _, x := range subnetsStore.List() {
				s.Subnets = append(s.Subnets, settings.Subnet{CIDR: x.CIDR, Enabled: x.Enabled, Note: x.Note})
			}
		})
		w.WriteHeader(http.StatusNoContent)
	})

	// Start/Stop scan
	r.Post("/api/subnets/{id}/scan", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		sub, ok := subnetsStore.Get(id)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		scanMu.Lock()
		if _, exists := scans[id]; exists {
			scanMu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			return
		}
		scanCtx, cancel := context.WithCancel(rootCtx)
		scans[id] = scanJob{cancel: cancel}
		scanMu.Unlock()

		subnetsStore.SetScanState(id, true, 0, time.Time{})

		go runScan(scanCtx, id, sub.CIDR)

		w.WriteHeader(http.StatusAccepted)
	})

	r.Post("/api/subnets/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		scanMu.Lock()
		j, ok := scans[id]
		if ok {
			j.cancel()
			delete(scans, id)
		}
		scanMu.Unlock()
		if ok {
			subnetsStore.SetScanState(id, false, 0, time.Now().UTC())
		}
		w.WriteHeader(http.StatusAccepted)
	})

	// Scan/Stop all (for Devices page control)
	r.Post("/api/subnets/scan_all", func(w http.ResponseWriter, r *http.Request) {
		for _, sn := range subnetsStore.List() {
			if !sn.Enabled {
				continue
			}
			// fire and forget; handler is idempotent due to scans map check
			req, _ := http.NewRequestWithContext(r.Context(), "POST", fmt.Sprintf("/api/subnets/%d/scan", sn.ID), nil)
			_ = req
			// call internal logic by invoking same code path: just do a minimal inline copy
			id := sn.ID
			scanMu.Lock()
			if _, exists := scans[id]; exists {
				scanMu.Unlock()
				continue
			}
			scanCtx, cancel := context.WithCancel(rootCtx)
			scans[id] = scanJob{cancel: cancel}
			scanMu.Unlock()
			subnetsStore.SetScanState(id, true, 0, time.Time{})
			go runScan(scanCtx, id, sn.CIDR)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	r.Post("/api/subnets/stop_all", func(w http.ResponseWriter, r *http.Request) {
		scanMu.Lock()
		for id, j := range scans {
			j.cancel()
			delete(scans, id)
			subnetsStore.SetScanState(id, false, 0, time.Now().UTC())
		}
		scanMu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})

	r.Get("/api/stream/devices", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusBadRequest)
			return
		}

		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-cache")
		w.Header().Set("connection", "keep-alive")

		ctx := r.Context()
		ch := store.Subscribe(ctx)

		send := func() {
			b, _ := json.Marshal(store.List())
			_, _ = fmt.Fprintf(w, "event: devices\ndata: %s\n\n", b)
			flusher.Flush()
		}

		send()

		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				send()
			case <-heartbeat.C:
				_, _ = fmt.Fprint(w, "event: ping\ndata: 1\n\n")
				flusher.Flush()
			}
		}
	})

	r.Get("/api/stream/subnets", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusBadRequest)
			return
		}

		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-cache")
		w.Header().Set("connection", "keep-alive")

		ctx := r.Context()
		ch := subnetsStore.Subscribe(ctx)

		send := func() {
			b, _ := json.Marshal(subnetsStore.List())
			_, _ = fmt.Fprintf(w, "event: subnets\ndata: %s\n\n", b)
			flusher.Flush()
		}
		send()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				send()
			}
		}
	})

	// UI (embedded)
	if uiFS, err := webui.FS(); err == nil {
		fileServer := http.FileServer(http.FS(uiFS))
		r.Handle("/*", fileServer)
	} else {
		log.Warn("web ui disabled", zap.Error(err))
	}

	addr := cfgStore.Get().HTTPAddr
	ln, actualAddr, err := listenWithFallback(addr)
	if err != nil {
		log.Fatal("http listen", zap.String("addr", addr), zap.Error(err))
	}
	if actualAddr != addr {
		log.Warn("http addr was busy; switched", zap.String("from", addr), zap.String("to", actualAddr))
		_ = cfgStore.Patch(func(s *settings.Settings) { s.HTTPAddr = actualAddr })
	}
	srv := &http.Server{Handler: r}
	go func() {
		log.Info("core http listening", zap.String("addr", actualAddr))
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http serve", zap.Error(err))
			select {
			case exitCh <- struct{}{}:
			default:
			}
		}
	}()

	// Wait for exit signal
	select {
	case <-rootCtx.Done():
	case <-exitCh:
	}

	// Stop scans
	scanMu.Lock()
	for _, j := range scans {
		j.cancel()
	}
	scans = map[int64]scanJob{}
	scanMu.Unlock()

	// Stop NATS client
	natsConnected.Store(false)
	natsMu.Lock()
	if natsClient != nil {
		_ = natsClient.Close()
		natsClient = nil
	}
	natsMu.Unlock()

	// Stop embedded NATS
	embMu.Lock()
	if emb != nil {
		emb.Shutdown()
		emb = nil
	}
	embMu.Unlock()

	// Stop HTTP
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = srv.Shutdown(ctxTimeout)
	cancel()
}

func listenWithFallback(addr string) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, addr, nil
	}

	// Try port+1..port+20 on "address already in use" only.
	if !isAddrInUse(err) {
		return nil, "", err
	}

	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		// handle ":8080" which SplitHostPort accepts, but keep safe
		if len(addr) > 0 && addr[0] == ':' {
			host = ""
			portStr = addr[1:]
		} else {
			return nil, "", err
		}
	}
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	if port == 0 {
		return nil, "", err
	}

	for i := 1; i <= 20; i++ {
		tryAddr := net.JoinHostPort(host, fmt.Sprintf("%d", port+i))
		ln, e := net.Listen("tcp", tryAddr)
		if e == nil {
			return ln, tryAddr, nil
		}
	}
	return nil, "", err
}

func isAddrInUse(err error) bool {
	// Windows error message contains this phrase; keep it simple.
	return strings.Contains(strings.ToLower(err.Error()), "only one usage of each socket address")
}
