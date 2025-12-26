package api

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

// createHTTPTransport creates an HTTP transport with optional proxy support
// Priority: HTTP_PROXY env var > HTTPS_PROXY env var > default proxy (127.0.0.1:15236)
func createHTTPTransport() *http.Transport {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		MaxConnsPerHost:     50,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	}

	// Check for proxy configuration from environment variables first
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("http_proxy")
	}
	if proxyURL == "" {
		// Try HTTPS_PROXY as well
		proxyURL = os.Getenv("HTTPS_PROXY")
		if proxyURL == "" {
			proxyURL = os.Getenv("https_proxy")
		}
	}

	// Default proxy if no environment variable is set
	if proxyURL == "" {
		proxyURL = "http://127.0.0.1:15236"
		log.Printf("[Proxy] Using default proxy: %s", proxyURL)
	} else {
		log.Printf("[Proxy] Using proxy from environment: %s", proxyURL)
	}

	// If proxy is configured, set it
	if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			transport.Proxy = http.ProxyURL(parsedURL)
			log.Printf("[Proxy] Proxy configured: %s", parsedURL.Host)
		} else {
			log.Printf("[Proxy] Warning: Failed to parse proxy URL %s: %v", proxyURL, err)
		}
	}

	return transport
}

// getProxyURL returns the proxy URL to use
// Priority: HTTP_PROXY env var > HTTPS_PROXY env var > default proxy (127.0.0.1:15236)
func getProxyURL() string {
	// Check for proxy configuration from environment variables first
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("http_proxy")
	}
	if proxyURL == "" {
		// Try HTTPS_PROXY as well
		proxyURL = os.Getenv("HTTPS_PROXY")
		if proxyURL == "" {
			proxyURL = os.Getenv("https_proxy")
		}
	}

	// Default proxy if no environment variable is set
	if proxyURL == "" {
		proxyURL = "http://127.0.0.1:15236"
		log.Printf("[Proxy] Using default proxy: %s", proxyURL)
	} else {
		log.Printf("[Proxy] Using proxy from environment: %s", proxyURL)
	}

	return proxyURL
}

// getWebSocketProxy returns a proxy function for WebSocket dialer
func getWebSocketProxy() func(*http.Request) (*url.URL, error) {
	proxyURL := getProxyURL()
	if proxyURL == "" {
		return nil
	}

	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		log.Printf("[Proxy] Warning: Failed to parse proxy URL %s: %v", proxyURL, err)
		return nil
	}

	log.Printf("[Proxy] WebSocket proxy configured: %s", parsedURL.Host)
	return http.ProxyURL(parsedURL)
}
