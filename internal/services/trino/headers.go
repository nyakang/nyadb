package trino

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validHeader(s string) bool {
	return utf8.ValidString(s) && len(s) <= 64<<10 && strings.IndexFunc(s, unicode.IsControl) < 0
}

func pairs(h http.Header, name string) (map[string]string, error) {
	m := map[string]string{}
	for _, line := range h.Values(name) {
		for _, pair := range strings.Split(line, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
			decoded, e := url.QueryUnescape(k)
			if !ok || decoded == "" || e != nil || !validHeader(decoded) {
				return nil, ErrProtocol
			}
			if _, e = url.QueryUnescape(v); e != nil {
				return nil, ErrProtocol
			}
			m[k] = v
		}
	}
	return m, nil
}

func (c *Client) applyPairs(h http.Header, request, added, removed string) error {
	m, err := pairs(c.headers, request)
	if err != nil {
		return err
	}
	changes, err := pairs(h, added)
	if err != nil {
		return err
	}
	for k, v := range changes {
		m[k] = v
	}
	for _, line := range h.Values(removed) {
		for _, key := range strings.Split(line, ",") {
			delete(m, strings.TrimSpace(key))
		}
	}
	if len(m) > 512 {
		return ErrBounds
	}
	c.headers.Del(request)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		c.headers.Add(request, k+"="+m[k])
	}
	return nil
}

func (c *Client) applyHeaders(h http.Header) error {
	for k, vs := range h {
		if strings.HasPrefix(strings.ToLower(k), "x-trino-") {
			for _, v := range vs {
				if !validHeader(v) {
					return ErrProtocol
				}
			}
		}
	}
	for _, suffix := range []string{"Catalog", "Schema", "Path"} {
		if values, ok := h[http.CanonicalHeaderKey("X-Trino-Set-"+suffix)]; ok {
			if len(values) != 1 {
				return ErrProtocol
			}
			c.headers.Set("X-Trino-"+suffix, values[0])
		}
	}
	if err := c.applyPairs(h, "X-Trino-Session", "X-Trino-Set-Session", "X-Trino-Clear-Session"); err != nil {
		return err
	}
	if err := c.applyPairs(h, "X-Trino-Role", "X-Trino-Set-Role", ""); err != nil {
		return err
	}
	if err := c.applyPairs(h, "X-Trino-Prepared-Statement", "X-Trino-Added-Prepare", "X-Trino-Deallocated-Prepare"); err != nil {
		return err
	}
	if v := h.Get("X-Trino-Started-Transaction-Id"); v != "" {
		c.headers.Set("X-Trino-Transaction-Id", v)
	}
	if _, ok := h[http.CanonicalHeaderKey("X-Trino-Clear-Transaction-Id")]; ok {
		c.headers.Set("X-Trino-Transaction-Id", "NONE")
	}
	if v := h.Get("X-Trino-Set-Authorization-User"); v != "" {
		if c.headers.Get("X-Trino-Original-User") == "" {
			c.headers.Set("X-Trino-Original-User", c.headers.Get("X-Trino-User"))
		}
		c.headers.Set("X-Trino-User", v)
	}
	if vs := h.Values("X-Trino-Set-Original-Roles"); len(vs) > 0 {
		c.headers[http.CanonicalHeaderKey("X-Trino-Original-Roles")] = append([]string(nil), vs...)
	}
	if h.Get("X-Trino-Reset-Authorization-User") == "true" {
		c.headers.Set("X-Trino-User", c.config.Username)
		c.headers.Del("X-Trino-Original-User")
		c.headers.Del("X-Trino-Original-Roles")
	}
	if h.Get("X-Trino-Query-Data-Encoding") != "" {
		return ErrProtocol
	} // Spooling is not requested or implemented.
	total := 0
	for k, vs := range c.headers {
		for _, v := range vs {
			total += len(k) + len(v) + 4
		}
	}
	if total > 64<<10 {
		return ErrBounds
	}
	return nil
}
