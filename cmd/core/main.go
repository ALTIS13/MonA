package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

	"asic-control/internal/antminer/httpapi"
	"asic-control/internal/bus/embeddednats"
	"asic-control/internal/bus/natsjs"
	"asic-control/internal/core/registry"
	"asic-control/internal/core/webui"
	"asic-control/internal/defaultcreds"
	"asic-control/internal/discovery/scanner"
	"asic-control/internal/discovery/subnets"
	"asic-control/internal/events"
	"asic-control/internal/logging"
	"asic-control/internal/modelnorm"
	"asic-control/internal/netutil"
	"asic-control/internal/secrets"
	"asic-control/internal/settings"
	whhttp "asic-control/internal/whatsminer/httpapi"
	vnishhttp "asic-control/internal/vnish/httpapi"
	"asic-control/internal/version"
)

func urlUserPass(user, pass string) string {
	// URL-encode to avoid breaking the redirect URL.
	if pass == "" {
		return url.PathEscape(user)
	}
	return url.PathEscape(user) + ":" + url.PathEscape(pass)
}

func main() {
	log, err := logging.New(logging.Config{Level: "info"})
	if err != nil {
		panic(err)
	}
	startedAt := time.Now()
	defer func() { _ = log.Sync() }()

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfgStore, err := settings.Open("data")
	if err != nil {
		log.Fatal("settings open", zap.Error(err))
	}
	sec, err := secrets.Open("data")
	if err != nil {
		log.Fatal("secrets open", zap.Error(err))
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

	// Auto enrichment (HTTP deep probe) worker pool.
	// Goal: devices should populate details automatically without manual clicks.
	type probeReq struct {
		IP     string
		Reason string
	}
	probeCh := make(chan probeReq, 8192)
	var probeMu sync.Mutex
	probeNext := map[string]time.Time{} // ip -> next allowed probe time (backoff)

	enqueueProbe := func(ip, reason string, minInterval time.Duration) {
		if ip == "" {
			return
		}
		if minInterval <= 0 {
			minInterval = 30 * time.Second
		}
		now := time.Now()
		probeMu.Lock()
		next := probeNext[ip]
		if now.Before(next) {
			probeMu.Unlock()
			return
		}
		probeNext[ip] = now.Add(minInterval)
		probeMu.Unlock()
		select {
		case probeCh <- probeReq{IP: ip, Reason: reason}:
		default:
			// drop if saturated (will retry on ticker)
		}
	}

	probeTimeoutFor := func(d *registry.Device) time.Duration {
		// Adaptive timeout: reduce flakiness on busy fleets and slow HTTP stacks.
		if d == nil {
			return 6 * time.Second
		}
		err := strings.ToLower(d.AuthError)
		fw := strings.ToLower(d.Firmware)
		if strings.Contains(err, "deadline exceeded") || strings.Contains(err, "timeout") {
			return 9 * time.Second
		}
		if strings.Contains(fw, "anthill") || strings.Contains(fw, "vnish") {
			return 8 * time.Second
		}
		if strings.ToLower(d.AuthStatus) == "ok" {
			return 4 * time.Second
		}
		return 6 * time.Second
	}

	// Build credential candidates for a device. Tries all enabled creds first (priority desc),
	// then optionally defaults (Settings.TryDefaultCreds).
	buildCreds := func(d *registry.Device) []httpapi.Cred {
		cfg := cfgStore.Get()
		type cand struct {
			pri  int
			fw   string
			cred httpapi.Cred
		}
		var cands []cand
		dv := strings.ToLower(strings.TrimSpace(d.Vendor))
		devFW := strings.ToLower(strings.TrimSpace(d.Firmware))
		devClass := "stock"
		if strings.Contains(devFW, "vnish") || strings.Contains(devFW, "anthill") || strings.Contains(devFW, "brains") {
			devClass = "vnish"
		}
		for _, c := range cfg.Credentials {
			if !c.Enabled {
				continue
			}
			cv := strings.ToLower(strings.TrimSpace(c.Vendor))
			if dv != "" && dv != "unknown" && dv != "asic" && cv != "" && cv != dv {
				continue
			}
			user, err := sec.DecryptString(c.UsernameEnc)
			if err != nil {
				continue
			}
			pass, err := sec.DecryptString(c.PasswordEnc)
			if err != nil {
				continue
			}
			cands = append(cands, cand{
				pri: c.Priority,
				fw:  strings.ToLower(strings.TrimSpace(c.Firmware)),
				cred: httpapi.Cred{
					Name:     c.Name,
					Username: user,
					Password: pass,
				},
			})
		}
		// Sort by (firmware match) then priority.
		for i := 0; i < len(cands); i++ {
			for j := i + 1; j < len(cands); j++ {
				si := 0
				sj := 0
				if cands[i].fw != "" && cands[i].fw == devClass {
					si += 1000
				}
				if cands[j].fw != "" && cands[j].fw == devClass {
					sj += 1000
				}
				si += cands[i].pri
				sj += cands[j].pri
				if sj > si {
					cands[i], cands[j] = cands[j], cands[i]
				}
			}
		}

		out := make([]httpapi.Cred, 0, len(cands)+10)

		// btcTools-like: for stock antminer, try known stock pairs first (before custom)
		// to reduce operator friction. Only when device does NOT look like vnish.
		if devClass == "stock" && (dv == "" || dv == "antminer" || dv == "asic" || dv == "unknown") {
			out = append(out,
				httpapi.Cred{Name: "stock:root/root", Username: "root", Password: "root"},
				httpapi.Cred{Name: "stock:root/admin", Username: "root", Password: "admin"},
			)
		}

		for _, x := range cands {
			out = append(out, x.cred)
		}

		// Optionally, built-in defaults list (currently empty unless you re-add it later)
		if cfg.TryDefaultCreds {
			for _, dc := range defaultcreds.Defaults() {
				if dc.Vendor != "generic" && dc.Vendor != dv {
					continue
				}
				out = append(out, httpapi.Cred{Name: "default:" + dc.Vendor, Username: dc.Username, Password: dc.Password})
			}
		}

		if len(out) == 0 {
			out = append(out, httpapi.Cred{Name: "no-auth"})
		}
		return out
	}

	toVnishCreds := func(in []httpapi.Cred) []vnishhttp.Cred {
		out := make([]vnishhttp.Cred, 0, len(in))
		for _, c := range in {
			out = append(out, vnishhttp.Cred{Name: c.Name, Username: c.Username, Password: c.Password})
		}
		return out
	}
	toWhatsCreds := func(in []httpapi.Cred) []whhttp.Cred {
		out := make([]whhttp.Cred, 0, len(in))
		for _, c := range in {
			out = append(out, whhttp.Cred{Name: c.Name, Username: c.Username, Password: c.Password})
		}
		return out
	}

	runProbe := func(ctx context.Context, ip string) httpapi.ProbeResult {
		d, ok := store.Get(ip)
		if !ok {
			return httpapi.ProbeResult{OK: false, Error: "not found"}
		}
		if !d.Online {
			store.UpdateEnrichment(ip, func(dd *registry.Device) {
				dd.AuthStatus = "fail"
				dd.AuthUpdated = time.Now().UTC()
				dd.AuthError = "offline"
			})
			return httpapi.ProbeResult{OK: false, Error: "offline"}
		}
		// mark trying (minimal UI indicator)
		store.UpdateEnrichment(ip, func(dd *registry.Device) {
			dd.AuthStatus = "trying"
			dd.AuthUpdated = time.Now().UTC()
			dd.AuthError = ""
		})
		v := strings.ToLower(strings.TrimSpace(d.Vendor))
		// whatsminer probe
		if v == "whatsminer" {
			creds := buildCreds(d)
			schemes := []string{"http"}
			wres := whhttp.Probe(ctx, ip, toWhatsCreds(creds), schemes)
			if wres.OK {
				f := whhttp.ExtractFacts(wres)
				store.UpdateEnrichment(ip, func(dd *registry.Device) {
					dd.AuthStatus = "ok"
					dd.AuthUpdated = time.Now().UTC()
					dd.AuthCredName = wres.UsedCred
					dd.AuthError = ""
					dd.Vendor = "whatsminer"
					if f.Model != "" {
						dd.Model = f.Model
					}
					if f.UptimeS > 0 {
						dd.UptimeS = f.UptimeS
					}
					if f.HashrateTHS > 0 {
						dd.HashrateTHS = f.HashrateTHS
					}
					if len(f.FansRPM) > 0 {
						dd.FansRPM = f.FansRPM
					}
					if len(f.TempsC) > 0 {
						dd.TempsC = f.TempsC
					}
				})
				return httpapi.ProbeResult{OK: true, Scheme: wres.Scheme, UsedCred: wres.UsedCred, Error: "", Responses: map[string]any{"whatsminer": wres.Responses}}
			}
			store.UpdateEnrichment(ip, func(dd *registry.Device) {
				dd.AuthStatus = "fail"
				dd.AuthUpdated = time.Now().UTC()
				dd.AuthCredName = wres.UsedCred
				dd.AuthError = wres.Error
			})
			return httpapi.ProbeResult{OK: false, Error: wres.Error}
		}

		// antminer first (stock JSON CGI) but if device looks like vnish/anthill, try vnish probe.
		if v != "antminer" && v != "asic" && v != "" && v != "unknown" {
			store.UpdateEnrichment(ip, func(dd *registry.Device) {
				dd.AuthStatus = "fail"
				dd.AuthUpdated = time.Now().UTC()
				dd.AuthError = "unsupported vendor"
			})
			return httpapi.ProbeResult{OK: false, Error: "unsupported vendor (antminer first)"}
		}
		creds := buildCreds(d)
		// btcTools-like: only try https if 443 is actually open (avoid hanging TLS on ASICs).
		schemes := []string{}
		for _, p := range d.OpenPorts {
			if p == 80 {
				schemes = append(schemes, "http")
				break
			}
		}
		has443 := false
		for _, p := range d.OpenPorts {
			if p == 443 {
				has443 = true
				break
			}
		}
		if len(schemes) == 0 {
			// If we didn't probe ports (or only https farms), default to http first (fast fail).
			schemes = []string{"http"}
		}
		if has443 {
			schemes = append(schemes, "https")
		}

		isAnthill := strings.Contains(strings.ToLower(d.Firmware), "anthill")

		res := httpapi.ProbeAntminerSchemes(ctx, ip, creds, schemes)
		if res.OK {
			facts := httpapi.ExtractFacts(res)
			// If we authenticated but still extracted nothing useful, mark FAIL (so operator can click in).
			if facts.MAC == "" && facts.Worker == "" && facts.Firmware == "" && facts.Model == "" && facts.HashrateTHS == 0 && facts.UptimeS == 0 && len(facts.FansRPM) == 0 && len(facts.TempsC) == 0 {
				store.UpdateEnrichment(ip, func(dd *registry.Device) {
					dd.AuthStatus = "fail"
					dd.AuthUpdated = time.Now().UTC()
					dd.AuthCredName = res.UsedCred
					dd.AuthError = "ok but no parsable data"
				})
				return res
			}

			store.UpdateEnrichment(ip, func(dd *registry.Device) {
				dd.AuthStatus = "ok"
				dd.AuthUpdated = time.Now().UTC()
				dd.AuthCredName = res.UsedCred
				dd.AuthError = ""
				if dd.Vendor == "" || dd.Vendor == "unknown" || dd.Vendor == "asic" {
					dd.Vendor = "antminer"
				}
				if dd.MAC == "" && facts.MAC != "" {
					dd.MAC = facts.MAC
				}
				if facts.Model != "" {
					n := modelnorm.Normalize(facts.Model)
					if n.Model != "" {
						dd.Model = n.Model
						if (dd.Vendor == "" || dd.Vendor == "unknown" || dd.Vendor == "asic") && n.Vendor != "unknown" {
							dd.Vendor = n.Vendor
						}
					} else {
						dd.Model = facts.Model
					}
				}
				if facts.Firmware != "" {
					dd.Firmware = facts.Firmware
				}
				if facts.Worker != "" {
					dd.Worker = facts.Worker
				}
				if facts.UptimeS > 0 {
					dd.UptimeS = facts.UptimeS
				}
				if facts.HashrateTHS > 0 {
					dd.HashrateTHS = facts.HashrateTHS
				}
				if len(facts.FansRPM) > 0 {
					dd.FansRPM = facts.FansRPM
				}
				if len(facts.TempsC) > 0 {
					dd.TempsC = facts.TempsC
				}
			})
		} else {
			// If CGI is not JSON API (e.g. Anthill/Vnish SPA), try vnish/anthill API probe.
			if isAnthill || strings.Contains(strings.ToLower(res.Error), "html response") || strings.Contains(strings.ToLower(d.Firmware), "vnish") {
				vres := vnishhttp.Probe(ctx, ip, toVnishCreds(creds), schemes)
				if vres.OK {
					f := vnishhttp.ExtractFacts(vres)
					store.UpdateEnrichment(ip, func(dd *registry.Device) {
						dd.AuthStatus = "ok"
						dd.AuthUpdated = time.Now().UTC()
						dd.AuthCredName = vres.UsedCred
						dd.AuthError = ""
						if dd.Vendor == "" || dd.Vendor == "unknown" || dd.Vendor == "asic" {
							dd.Vendor = "antminer"
						}
						if f.Model != "" {
							n := modelnorm.Normalize(f.Model)
							if n.Model != "" {
								dd.Model = n.Model
							} else {
								dd.Model = f.Model
							}
						}
						if f.Firmware != "" {
							dd.Firmware = f.Firmware
						}
						if f.Worker != "" {
							dd.Worker = f.Worker
						}
						if f.UptimeS > 0 {
							dd.UptimeS = f.UptimeS
						}
						if f.HashrateTHS > 0 {
							dd.HashrateTHS = f.HashrateTHS
						}
						if len(f.FansRPM) > 0 {
							dd.FansRPM = f.FansRPM
						}
						if len(f.TempsC) > 0 {
							dd.TempsC = f.TempsC
						}
					})
					return httpapi.ProbeResult{OK: true, Scheme: vres.Scheme, UsedCred: vres.UsedCred, Error: "", Responses: map[string]any{"vnish": vres.Responses}}
				}
			}
			// Avoid auth flapping: do not downgrade OK->FAIL on transient errors.
			store.UpdateEnrichment(ip, func(dd *registry.Device) {
				if strings.ToLower(dd.AuthStatus) == "ok" && time.Since(dd.AuthUpdated) < 10*time.Minute {
					dd.AuthError = res.Error
					return
				}
				dd.AuthStatus = "fail"
				dd.AuthUpdated = time.Now().UTC()
				dd.AuthCredName = res.UsedCred
				dd.AuthError = res.Error
			})
		}
		return res
	}

	// workers (faster enrichment for large fleets; bounded by per-IP backoff)
	workers := 48
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case <-rootCtx.Done():
					return
				case req := <-probeCh:
					var d *registry.Device
					if dd, ok := store.Get(req.IP); ok {
						d = dd
					}
					ctx, cancel := context.WithTimeout(rootCtx, probeTimeoutFor(d))
					_ = runProbe(ctx, req.IP)
					cancel()
				}
			}
		}()
	}

	// periodic retry: pick devices that are online and missing key fields.
	go func() {
		t := time.NewTicker(12 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-t.C:
				for _, d := range store.List() {
					// only re-probe if likely needed
					if !d.Online {
						continue
					}
					if strings.ToLower(d.Vendor) != "antminer" && strings.ToLower(d.Vendor) != "asic" && d.Vendor != "" {
						continue
					}
					need := d.MAC == "" || d.Worker == "" || d.Firmware == "" || len(d.FansRPM) == 0
					if !need {
						continue
					}
					enqueueProbe(d.IP, "tick", 60*time.Second)
				}
			}
		}
	}()

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
					if res.Vendor != "" {
						d.Vendor = res.Vendor
					}
					if res.Model != "" {
						d.Model = res.Model
					}
					if res.Firmware != "" {
						d.Firmware = res.Firmware
					}
					if res.Worker != "" {
						d.Worker = res.Worker
					}
					if res.UptimeS > 0 {
						d.UptimeS = res.UptimeS
					}
					if res.HashrateTHS > 0 {
						d.HashrateTHS = res.HashrateTHS
					}
					if len(res.FansRPM) > 0 {
						d.FansRPM = res.FansRPM
					}
					if len(res.TempsC) > 0 {
						d.TempsC = res.TempsC
					}
				})

				if !res.IsASIC {
					return
				}
				// Auto-enrich immediately after discovery (low frequency; worker pool handles backoff).
				enqueueProbe(ip, "scan", 15*time.Second)
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
			"started_at":     startedAt.Format(time.RFC3339),
			"uptime_s":       int64(time.Since(startedAt).Seconds()),
		})
	})
	r.Get("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(store.List())
	})

	// Device details (light) + deep probe (Antminer first)
	r.Get("/api/devices/{ip}", func(w http.ResponseWriter, r *http.Request) {
		ip := strings.TrimSpace(chi.URLParam(r, "ip"))
		w.Header().Set("content-type", "application/json")
		if d, ok := store.Get(ip); ok {
			_ = json.NewEncoder(w).Encode(d)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	r.Post("/api/devices/{ip}/probe", func(w http.ResponseWriter, r *http.Request) {
		ip := strings.TrimSpace(chi.URLParam(r, "ip"))
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		res := runProbe(ctx, ip)
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})

	// Open miner UI with auto-login (best-effort).
	// Uses the last successful credential for the device (AuthStatus==ok).
	// For BasicAuth targets, redirects to http://user:pass@ip/.
	// NOTE: some modern browsers may restrict credential-in-URL, but many farm setups still allow it.
	r.Get("/ui/open/{ip}", func(w http.ResponseWriter, r *http.Request) {
		ip := strings.TrimSpace(chi.URLParam(r, "ip"))
		d, ok := store.Get(ip)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if !d.Online {
			http.Redirect(w, r, "http://"+ip+"/", http.StatusFound)
			return
		}
		if strings.ToLower(d.AuthStatus) != "ok" || strings.TrimSpace(d.AuthCredName) == "" {
			http.Redirect(w, r, "http://"+ip+"/", http.StatusFound)
			return
		}

		user := ""
		pass := ""
		name := d.AuthCredName

		// stock pairs
		if strings.HasPrefix(name, "stock:") {
			// stock:root/root
			rest := strings.TrimPrefix(name, "stock:")
			parts := strings.Split(rest, "/")
			if len(parts) == 2 {
				user, pass = parts[0], parts[1]
			}
		} else {
			cfg := cfgStore.Get()
			for _, c := range cfg.Credentials {
				if !c.Enabled {
					continue
				}
				if c.Name != name {
					continue
				}
				u, err1 := sec.DecryptString(c.UsernameEnc)
				p, err2 := sec.DecryptString(c.PasswordEnc)
				if err1 == nil && err2 == nil {
					user, pass = u, p
					break
				}
			}
		}

		if user == "" {
			http.Redirect(w, r, "http://"+ip+"/", http.StatusFound)
			return
		}
		http.Redirect(w, r, "http://"+urlUserPass(user, pass)+"@"+ip+"/", http.StatusFound)
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
		// Settings UI does not edit credentials; never allow wiping them.
		prev := cfgStore.Get()
		s.Credentials = prev.Credentials
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

	// Credentials (stored encrypted in settings.json; secrets in data/secret.key)
	type credPublic struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Vendor   string `json:"vendor"`
		Firmware string `json:"firmware,omitempty"`
		Enabled  bool   `json:"enabled"`
		Priority int    `json:"priority"`
		Note     string `json:"note,omitempty"`
	}
	newID := func() string {
		var b [8]byte
		_, _ = rand.Read(b[:])
		return fmt.Sprintf("%x", b[:])
	}
	r.Get("/api/creds", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		cfg := cfgStore.Get()
		out := make([]credPublic, 0, len(cfg.Credentials))
		for _, c := range cfg.Credentials {
			out = append(out, credPublic{
				ID:       c.ID,
				Name:     c.Name,
				Vendor:   c.Vendor,
				Firmware: c.Firmware,
				Enabled:  c.Enabled,
				Priority: c.Priority,
				Note:     c.Note,
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	r.Post("/api/creds", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name     string `json:"name"`
			Vendor   string `json:"vendor"`
			Firmware string `json:"firmware"`
			Enabled  bool   `json:"enabled"`
			Priority int    `json:"priority"`
			Note     string `json:"note"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		req.Vendor = strings.TrimSpace(strings.ToLower(req.Vendor))
		req.Firmware = strings.TrimSpace(strings.ToLower(req.Firmware))
		if req.Name == "" || req.Vendor == "" {
			http.Error(w, "name and vendor required", http.StatusBadRequest)
			return
		}
		uEnc, err := sec.EncryptString(req.Username)
		if err != nil {
			http.Error(w, "encrypt username failed", http.StatusInternalServerError)
			return
		}
		pEnc, err := sec.EncryptString(req.Password)
		if err != nil {
			http.Error(w, "encrypt password failed", http.StatusInternalServerError)
			return
		}
		id := newID()
		_ = cfgStore.Patch(func(s *settings.Settings) {
			s.Credentials = append(s.Credentials, settings.Credential{
				ID:          id,
				Name:        req.Name,
				Vendor:      req.Vendor,
				Firmware:    req.Firmware,
				Enabled:     req.Enabled,
				Priority:    req.Priority,
				Note:        req.Note,
				UsernameEnc: uEnc,
				PasswordEnc: pEnc,
			})
		})
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	})
	r.Patch("/api/creds/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req struct {
			Name     *string `json:"name"`
			Vendor   *string `json:"vendor"`
			Firmware *string `json:"firmware"`
			Enabled  *bool   `json:"enabled"`
			Priority *int    `json:"priority"`
			Note     *string `json:"note"`
			Username *string `json:"username"`
			Password *string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		var updated bool
		_ = cfgStore.Patch(func(s *settings.Settings) {
			for i := range s.Credentials {
				if s.Credentials[i].ID != id {
					continue
				}
				c := &s.Credentials[i]
				if req.Name != nil {
					c.Name = strings.TrimSpace(*req.Name)
				}
				if req.Vendor != nil {
					c.Vendor = strings.TrimSpace(strings.ToLower(*req.Vendor))
				}
				if req.Firmware != nil {
					c.Firmware = strings.TrimSpace(strings.ToLower(*req.Firmware))
				}
				if req.Enabled != nil {
					c.Enabled = *req.Enabled
				}
				if req.Priority != nil {
					c.Priority = *req.Priority
				}
				if req.Note != nil {
					c.Note = *req.Note
				}
				if req.Username != nil && strings.TrimSpace(*req.Username) != "" {
					if uEnc, err := sec.EncryptString(*req.Username); err == nil {
						c.UsernameEnc = uEnc
					}
				}
				if req.Password != nil && strings.TrimSpace(*req.Password) != "" {
					if pEnc, err := sec.EncryptString(*req.Password); err == nil {
						c.PasswordEnc = pEnc
					}
				}
				updated = true
			}
		})
		if !updated {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	r.Delete("/api/creds/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var removed bool
		_ = cfgStore.Patch(func(s *settings.Settings) {
			out := s.Credentials[:0]
			for _, c := range s.Credentials {
				if c.ID == id {
					removed = true
					continue
				}
				out = append(out, c)
			}
			s.Credentials = out
		})
		if !removed {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
