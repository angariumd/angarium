package netutils

import (
	"crypto/tls"
	"net/http"
)

// NewClient returns an http.Client with optional certificate verification skipping.
func NewClient(insecure bool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
		},
	}
}
