package netutils

import (
	"crypto/tls"
	"net/http"
)

// NewInsecureClient returns an http.Client that skips certificate verification.
// This is useful for self-signed certificates in a private network (MVP).
func NewInsecureClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// DefaultClient can be replaced with NewInsecureClient if we want global behavior.
var DefaultClient = NewInsecureClient()
