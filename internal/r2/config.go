// Package r2 is a minimal Cloudflare R2 (S3-compatible) client using stdlib
// AWS SigV4 signing. No bucket name or credentials are ever embedded in source;
// everything is loaded at runtime from the user's secret files / env vars.
package r2

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds R2 connection settings. Never commit a populated instance.
type Config struct {
	AccountID       string `json:"account_id"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Bucket          string `json:"bucket"`
	// EndpointURL optionally overrides the account-derived S3 endpoint (e.g. a
	// jurisdiction-specific host). Empty = derive from AccountID.
	EndpointURL string `json:"endpoint_url"`
	// PublicBaseURL is an optional public/CDN base (e.g. an r2.dev or custom
	// domain) used to build unauthenticated audio URLs. Empty = use presigning.
	PublicBaseURL string `json:"public_base_url"`
}

// Endpoint returns the S3 API endpoint for the account.
func (c *Config) Endpoint() string {
	if c.EndpointURL != "" {
		return strings.TrimRight(c.EndpointURL, "/")
	}
	return "https://" + c.AccountID + ".r2.cloudflarestorage.com"
}

// Host returns the endpoint host (no scheme), used for SigV4 signing.
func (c *Config) Host() string {
	h := c.Endpoint()
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return strings.TrimRight(h, "/")
}

func (c *Config) validate() error {
	var missing []string
	if c.AccountID == "" && c.EndpointURL == "" {
		missing = append(missing, "account_id or endpoint_url")
	}
	if c.AccessKeyID == "" {
		missing = append(missing, "access_key_id")
	}
	if c.SecretAccessKey == "" {
		missing = append(missing, "secret_access_key")
	}
	if c.Bucket == "" {
		missing = append(missing, "bucket")
	}
	if len(missing) > 0 {
		return fmt.Errorf("r2 config incomplete: missing %s (set via env R2_* or %s)",
			strings.Join(missing, ", "), configSearchHint())
	}
	return nil
}

// Access modes select which credential profile to load.
const (
	ModeReadOnly  = "read"
	ModeReadWrite = "rw"
)

// LoadConfig loads config without a per-file credential profile (JSON/env only).
func LoadConfig() (*Config, error) { return LoadConfigMode("") }

// LoadConfigMode assembles a Config from, in increasing precedence:
//  1. a JSON config file (R2_CONFIG, else ~/.parso-r2.json, else ./r2.config.json)
//  2. the account-id secret file ~/.cloudflare-r2-api-account-id
//  3. a per-mode credential profile of single-value files (see profilePrefix) —
//     access-key-id, secret-access-key, and url — for mode "read" or "rw"
//  4. R2_* environment variables
//
// All of these are gitignored / outside the repo. mode "" skips step 3.
func LoadConfigMode(mode string) (*Config, error) {
	cfg := &Config{}

	// (1) JSON config file.
	for _, path := range configPaths() {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var fromFile Config
		if err := json.Unmarshal(data, &fromFile); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		merge(cfg, &fromFile)
		break
	}

	// (2) Account-id secret file.
	if cfg.AccountID == "" {
		if data, err := os.ReadFile(accountIDFile()); err == nil {
			cfg.AccountID = strings.TrimSpace(string(data))
		}
	}

	// (3) Per-mode credential profile (read-only vs read-write).
	if mode != "" {
		loadProfile(cfg, mode)
	}

	// (4) Environment overrides.
	envMerge(cfg)

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadProfile reads single-value secret files for a mode. The path pattern is
// "<prefix><mode>-token-<field>" where prefix defaults to
// ~/.cloudflare-pdaudio- and is overridable via R2_PROFILE_PREFIX. Missing files
// are ignored so callers can mix profiles with JSON/env config.
func loadProfile(cfg *Config, mode string) {
	prefix := os.Getenv("R2_PROFILE_PREFIX")
	if prefix == "" {
		prefix = filepath.Join(homeDir(), ".cloudflare-pdaudio-")
	}
	read := func(field string) string {
		data, err := os.ReadFile(prefix + mode + "-token-" + field)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	if v := read("access-key-id"); v != "" {
		cfg.AccessKeyID = v
	}
	if v := read("secret-access-key"); v != "" {
		cfg.SecretAccessKey = v
	}
	if v := read("url"); v != "" {
		cfg.EndpointURL = v
	}
}

func merge(dst, src *Config) {
	if src.AccountID != "" {
		dst.AccountID = src.AccountID
	}
	if src.AccessKeyID != "" {
		dst.AccessKeyID = src.AccessKeyID
	}
	if src.SecretAccessKey != "" {
		dst.SecretAccessKey = src.SecretAccessKey
	}
	if src.Bucket != "" {
		dst.Bucket = src.Bucket
	}
	if src.PublicBaseURL != "" {
		dst.PublicBaseURL = src.PublicBaseURL
	}
}

func envMerge(cfg *Config) {
	if v := os.Getenv("R2_ACCOUNT_ID"); v != "" {
		cfg.AccountID = v
	}
	if v := os.Getenv("R2_ACCESS_KEY_ID"); v != "" {
		cfg.AccessKeyID = v
	}
	if v := os.Getenv("R2_SECRET_ACCESS_KEY"); v != "" {
		cfg.SecretAccessKey = v
	}
	if v := os.Getenv("R2_BUCKET"); v != "" {
		cfg.Bucket = v
	}
	if v := os.Getenv("R2_PUBLIC_BASE_URL"); v != "" {
		cfg.PublicBaseURL = v
	}
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func accountIDFile() string {
	return filepath.Join(homeDir(), ".cloudflare-r2-api-account-id")
}

func configPaths() []string {
	return []string{
		os.Getenv("R2_CONFIG"),
		filepath.Join(homeDir(), ".parso-r2.json"),
		"r2.config.json",
	}
}

func configSearchHint() string {
	return "~/.parso-r2.json or ./r2.config.json"
}
