// Package httpx builds HTTP clients with an optional proxy: the direct/proxy fallbacks
// in subscribe and upgrade used to duplicate the same Transport assembly — centralized
// here to avoid drift.
package httpx

import (
	"net/http"
	"net/url"
	"time"
)

// Transport returns an outbound Transport; a non-empty proxy routes through it (empty
// string = direct).
func Transport(proxy string) *http.Transport {
	tr := &http.Transport{}
	if proxy != "" {
		u, _ := url.Parse(proxy)
		tr.Proxy = http.ProxyURL(u)
	}
	return tr
}

// Client returns an http.Client with a timeout; a non-empty proxy routes outbound
// traffic through it.
func Client(proxy string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport(proxy)}
}
