package consul

import (
	"bytes"
	"net/url"
	"strconv"
	"strings"
)

// Consul cleans URL paths. Noncanonical keys use JSON transactions so they are
// preserved or explicitly rejected by Consul, never silently rewritten.
func canonicalHTTPKey(key string) bool {
	if key == "" {
		return true
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func KeyReadRequest(key string) (Request, error) {
	if !validKey(key, false) {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(key) {
		return TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "get", Key: key}}})
	}
	path, e := KeyPath(key)
	return Request{Method: "GET", Path: path}, e
}
func KeyListRequest(prefix string, keysOnly bool) (Request, error) {
	if !validKey(prefix, true) {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(prefix) {
		return TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "get-tree", Key: prefix}}})
	}
	path, e := KeyPath(prefix)
	query := url.Values{"recurse": {""}}
	if keysOnly {
		query = url.Values{"keys": {""}}
	}
	return Request{Method: "GET", Path: path, Query: query}, e
}
func KeyPutRequest(key string, value []byte, flags, index *uint64) (Request, error) {
	if !validKey(key, false) || len(value) > valueLimit {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(key) {
		body := bytes.Clone(value)
		if body == nil {
			body = []byte{}
		}
		verb := "set"
		if index != nil {
			verb = "cas"
		}
		return TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: verb, Key: key, Value: &body, Flags: flags, Index: index}}})
	}
	path, e := KeyPath(key)
	query := url.Values{}
	if flags != nil {
		query.Set("flags", strconv.FormatUint(*flags, 10))
	}
	if index != nil {
		query.Set("cas", strconv.FormatUint(*index, 10))
	}
	return Request{Method: "PUT", Path: path, Body: bytes.Clone(value), Query: query}, e
}
func KeyDeleteRequest(key string, index *uint64, recurse bool) (Request, error) {
	if !validKey(key, recurse) || recurse && index != nil {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(key) {
		verb := "delete"
		if index != nil {
			verb = "delete-cas"
		}
		if recurse {
			verb = "delete-tree"
		}
		return TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: verb, Key: key, Index: index}}})
	}
	path, e := KeyPath(key)
	query := url.Values{}
	if index != nil {
		query.Set("cas", strconv.FormatUint(*index, 10))
	}
	if recurse {
		query.Set("recurse", "")
	}
	return Request{Method: "DELETE", Path: path, Query: query}, e
}
