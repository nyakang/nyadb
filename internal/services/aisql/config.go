// Package aisql owns configurable Chat Completions transport and AI settings.
// It has no database, UI or storage dependencies and never executes SQL.
package aisql

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const PreferenceKey = "ai_sql_v1"

var (
	ErrConfig     = errors.New("invalid AI service configuration")
	ErrConflict   = errors.New("AI settings revision conflict")
	ErrKey        = errors.New("AI system keyring unavailable or key missing")
	ErrStorage    = errors.New("AI settings storage unavailable")
	ErrTransport  = errors.New("AI service request failed")
	ErrResponse   = errors.New("invalid AI service response")
	ErrIncomplete = errors.New("AI response incomplete or unsupported")
)

type Config struct {
	Version        int      `json:"version"`
	Revision       uint64   `json:"revision"`
	Endpoint       string   `json:"endpoint"`
	Model          string   `json:"model"`
	TokenParameter string   `json:"token_parameter"`
	MaxTokens      int      `json:"max_tokens"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Stream         bool     `json:"stream"`
	Anonymous      bool     `json:"anonymous"`
	KeyID          string   `json:"key_id,omitempty"`
	Retired        []string `json:"retired_keys,omitempty"`
}

func Defaults() Config {
	return Config{Version: 1, TokenParameter: "max_completion_tokens", MaxTokens: 2048, TimeoutSeconds: 90, Stream: true}
}
func (c Config) Clone() Config { c.Retired = append([]string(nil), c.Retired...); return c }

func Normalize(c Config) (Config, error) {
	c = c.Clone()
	c.Endpoint, c.Model = strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.Model)
	if c.Version != 1 || c.Model == "" || len(c.Model) > 256 || !utf8.ValidString(c.Model) || strings.IndexFunc(c.Model, unicode.IsControl) >= 0 || c.MaxTokens < 128 || c.MaxTokens > 32768 || c.TimeoutSeconds < 5 || c.TimeoutSeconds > 600 || (c.TokenParameter != "max_tokens" && c.TokenParameter != "max_completion_tokens") || len(c.Retired) > 16 {
		return c, ErrConfig
	}
	u, e := endpoint(c.Endpoint)
	if e != nil {
		return c, e
	}
	c.Endpoint = u.String()
	if !validID(c.KeyID) {
		return c, ErrConfig
	}
	seen := map[string]bool{}
	for _, id := range c.Retired {
		if id == "" || !validID(id) || id == c.KeyID || seen[id] {
			return c, ErrConfig
		}
		seen[id] = true
	}
	return c, nil
}
func validID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) != 32 {
		return false
	}
	for _, b := range id {
		if !strings.ContainsRune("0123456789abcdef", b) {
			return false
		}
	}
	return true
}
func endpoint(raw string) (*url.URL, error) {
	if len(raw) > 2048 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return nil, ErrConfig
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, ErrConfig
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, ErrConfig
		}
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path += "/chat/completions"
		u.RawPath = ""
	}
	return u, nil
}
func Origin(c Config) string {
	u, e := endpoint(c.Endpoint)
	if e != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
func Decode(raw string) (Config, error) {
	if raw == "" {
		return Defaults(), nil
	}
	var c Config
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 16<<10 || d.Decode(&c) != nil {
		return c, ErrConfig
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, ErrConfig
	}
	return Normalize(c)
}
