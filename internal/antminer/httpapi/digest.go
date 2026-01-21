package httpapi

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Minimal Digest auth (qop=auth) for miner web UIs (lighttpd).
// Not a full RFC implementation, but enough for common Antminer deployments.

type digestChallenge struct {
	Realm string
	Nonce string
	Qop   string
	Algo  string
	Opaque string
}

func parseDigestChallenge(h string) (digestChallenge, bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(h), "digest ") {
		return digestChallenge{}, false
	}
	h = strings.TrimSpace(h[len("Digest "):])
	parts := strings.Split(h, ",")
	var c digestChallenge
	for _, p := range parts {
		p = strings.TrimSpace(p)
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		v := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		switch k {
		case "realm":
			c.Realm = v
		case "nonce":
			c.Nonce = v
		case "qop":
			c.Qop = v
		case "algorithm":
			c.Algo = v
		case "opaque":
			c.Opaque = v
		}
	}
	if c.Realm == "" || c.Nonce == "" {
		return digestChallenge{}, false
	}
	return c, true
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func buildDigestAuth(username, password, method, uri string, c digestChallenge) string {
	// Only MD5 + qop=auth supported.
	ha1 := md5hex(username + ":" + c.Realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	nc := "00000001"
	cnonce := randHex(8)
	qop := "auth"
	if c.Qop != "" && !strings.Contains(strings.ToLower(c.Qop), "auth") {
		// fallback: no qop provided/usable
		qop = ""
	}

	var resp string
	if qop != "" {
		resp = md5hex(ha1 + ":" + c.Nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		resp = md5hex(ha1 + ":" + c.Nonce + ":" + ha2)
	}

	// Compose header
	var b strings.Builder
	b.WriteString(`Digest username="`)
	b.WriteString(username)
	b.WriteString(`", realm="`)
	b.WriteString(c.Realm)
	b.WriteString(`", nonce="`)
	b.WriteString(c.Nonce)
	b.WriteString(`", uri="`)
	b.WriteString(uri)
	b.WriteString(`", response="`)
	b.WriteString(resp)
	b.WriteString(`"`)
	if c.Opaque != "" {
		b.WriteString(`, opaque="`)
		b.WriteString(c.Opaque)
		b.WriteString(`"`)
	}
	if qop != "" {
		b.WriteString(`, qop=`)
		b.WriteString(qop)
		b.WriteString(`, nc=`)
		b.WriteString(nc)
		b.WriteString(`, cnonce="`)
		b.WriteString(cnonce)
		b.WriteString(`"`)
	}
	return b.String()
}

