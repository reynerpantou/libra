// Package config reads runtime configuration from environment variables, so
// the same binary runs locally and in the cloud unchanged.
package config

import (
	"log"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr         string
	DatabaseURL  string
	CookieSecure bool          // true when served over HTTPS
	SessionTTL   time.Duration // how long a sign-in lasts
	AdminUser    string        // the owner account created on first boot
	AdminEmail   string        // the owner's Google/Apple email

	// PipelineInterval is how often the data pipeline rolls up new
	// exposures and events. Zero disables the in-process scheduler (run
	// `libra pipeline` from cron instead).
	PipelineInterval time.Duration

	// TrustedProxies are reverse proxies whose X-Forwarded-For is believed.
	TrustedProxies []netip.Prefix

	// PublicURL is where people open Libra; sign-in callbacks return here.
	PublicURL string

	GoogleClientID     string
	GoogleClientSecret string
	AppleClientID      string
	AppleTeamID        string
	AppleKeyID         string
	ApplePrivateKey    string

	// Test-only: point a provider at a fake server.
	GoogleTestBase string
	AppleTestBase  string
}

func Load() Config {
	return Config{
		Addr:        env("LIBRA_ADDR", ":8080"),
		DatabaseURL: env("LIBRA_DATABASE_URL", "postgres://libra:libra@localhost:5432/libra?sslmode=disable"),
		// Secure by default. Browsers treat http://localhost as secure too.
		CookieSecure:     envBool("LIBRA_COOKIE_SECURE", true),
		SessionTTL:       time.Duration(envInt("LIBRA_SESSION_TTL_HOURS", 168)) * time.Hour,
		AdminUser:        env("LIBRA_ADMIN_USER", "admin"),
		AdminEmail:       strings.ToLower(strings.TrimSpace(env("LIBRA_ADMIN_EMAIL", ""))),
		PipelineInterval: time.Duration(envInt("LIBRA_PIPELINE_INTERVAL_SECONDS", 300)) * time.Second,
		TrustedProxies:   envPrefixes("LIBRA_TRUSTED_PROXIES"),

		PublicURL:          strings.TrimRight(env("LIBRA_PUBLIC_URL", "http://localhost:8080"), "/"),
		GoogleClientID:     env("LIBRA_GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: env("LIBRA_GOOGLE_CLIENT_SECRET", ""),
		AppleClientID:      env("LIBRA_APPLE_CLIENT_ID", ""),
		AppleTeamID:        env("LIBRA_APPLE_TEAM_ID", ""),
		AppleKeyID:         env("LIBRA_APPLE_KEY_ID", ""),
		ApplePrivateKey:    envOrFile("LIBRA_APPLE_PRIVATE_KEY"),
		GoogleTestBase:     env("LIBRA_GOOGLE_TEST_BASE", ""),
		AppleTestBase:      env("LIBRA_APPLE_TEST_BASE", ""),
	}
}

// envOrFile reads K, or the file named by K_FILE (for keys mounted as files).
func envOrFile(k string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	if path := os.Getenv(k + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("%s_FILE: %v", k, err)
		}
		return string(b)
	}
	return ""
}

// envPrefixes parses a comma-separated list of IPs and CIDRs.
func envPrefixes(k string) []netip.Prefix {
	var out []netip.Prefix
	for _, part := range strings.Split(os.Getenv(k), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(part); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		} else {
			log.Fatalf("%s: %q is not an IP or CIDR", k, part)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
