package httpingress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"
)

const (
	defaultConnectTimeout        = 5 * time.Second
	maximumConnectTimeout        = time.Minute
	defaultResponseHeaderTimeout = 30 * time.Second
	maximumResponseHeaderTimeout = 10 * time.Minute
	defaultIdleConnectionTimeout = 90 * time.Second
	maximumIdleConnectionTimeout = 30 * time.Minute
	defaultMaximumConnections    = 256
	hardMaximumConnections       = 4096
	defaultMaximumRequestBody    = int64(256 << 20)
	hardMaximumRequestBody       = int64(4 << 30)
)

var untrustedForwardingHeaders = []string{
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Port",
	"X-Forwarded-Proto",
	"X-Forwarded-Server",
	"X-Forwarded-Ssl",
	"X-Real-IP",
	"X-Url-Scheme",
	"Front-End-Https",
}

// ErrorObserver receives request-local proxy failures without gaining routing authority.
//
// Delivery is asynchronous and bounded; later diagnostics are dropped while one callback runs.
type ErrorObserver func(error)

// CertificateProvider returns the current certificate for one already-authorized exact host.
type CertificateProvider func(context.Context, string) (*tls.Certificate, error)

// Config defines upstream transport bounds shared by every exact ingress route.
type Config struct {
	// ConnectTimeout bounds each private-upstream TCP dial.
	// Zero selects Harbor's conservative default.
	ConnectTimeout time.Duration
	// ResponseHeaderTimeout bounds upstreams that accept a request but never begin a response.
	// Zero selects Harbor's conservative default.
	ResponseHeaderTimeout time.Duration
	// IdleConnectionTimeout controls reuse of inactive private-upstream connections.
	// Zero selects Harbor's conservative default.
	IdleConnectionTimeout time.Duration
	// MaxConnectionsPerHost prevents one project from consuming every ingress connection.
	// Zero selects Harbor's conservative default.
	MaxConnectionsPerHost int
	// MaxRequestBodyBytes bounds one streaming request body without buffering it in Harbor.
	// Zero selects Harbor's conservative default.
	MaxRequestBodyBytes int64
	// ObserveError optionally records request-local proxy and protocol-server failures.
	ObserveError ErrorObserver
}

// Router atomically routes HTTP requests and TLS handshakes through one immutable snapshot.
type Router struct {
	config    Config
	snapshot  atomic.Pointer[Snapshot]
	proxy     *httputil.ReverseProxy
	transport *http.Transport
	observer  chan struct{}
	dropped   atomic.Uint64
}

// routeContextKey prevents request input from manufacturing an already-authorized route.
type routeContextKey struct{}

// NewRouter validates transport policy and installs one complete initial routing snapshot.
func NewRouter(config Config, initial *Snapshot) (*Router, error) {
	if initial == nil {
		return nil, fmt.Errorf("initial ingress snapshot is required")
	}
	config = normalizeConfig(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	dialer := &net.Dialer{
		Timeout:   config.ConnectTimeout,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          config.MaxConnectionsPerHost,
		MaxIdleConnsPerHost:   config.MaxConnectionsPerHost,
		MaxConnsPerHost:       config.MaxConnectionsPerHost,
		IdleConnTimeout:       config.IdleConnectionTimeout,
		ResponseHeaderTimeout: config.ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
	}
	router := &Router{config: config, transport: transport}
	if config.ObserveError != nil {
		router.observer = make(chan struct{}, 1)
	}
	router.snapshot.Store(initial)
	router.proxy = &httputil.ReverseProxy{
		Rewrite:        router.rewrite,
		Transport:      transport,
		FlushInterval:  -1,
		ErrorHandler:   router.handleProxyError,
		ModifyResponse: router.validateUpstreamResponse,
	}
	return router, nil
}

// Replace atomically publishes a previously validated complete routing table.
func (router *Router) Replace(snapshot *Snapshot) error {
	if snapshot == nil {
		return fmt.Errorf("replacement ingress snapshot is required")
	}
	router.snapshot.Store(snapshot)
	return nil
}

// HTTPHandler redirects registered domains to HTTPS and rejects every unknown authority.
func (router *Router) HTTPHandler() http.Handler {
	return http.HandlerFunc(router.handleHTTP)
}

// HTTPSHandler proxies exact Host and SNI matches to their private loopback upstreams.
func (router *Router) HTTPSHandler() http.Handler {
	return http.HandlerFunc(router.handleHTTPS)
}

// TLSConfig rejects unknown SNI names before asking the certificate authority for key material.
func (router *Router) TLSConfig(provider CertificateProvider) (*tls.Config, error) {
	if provider == nil {
		return nil, fmt.Errorf("ingress certificate provider is required")
	}
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	}
	config.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		host, err := canonicalDomain(hello.ServerName)
		if err != nil {
			return nil, fmt.Errorf("reject TLS SNI: %w", err)
		}
		if _, found := router.snapshot.Load().Route(host); !found {
			return nil, fmt.Errorf("reject unregistered TLS SNI %q", host)
		}
		ctx := hello.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		certificate, err := provider(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("load certificate for %q: %w", host, err)
		}
		if certificate == nil {
			return nil, fmt.Errorf("load certificate for %q: provider returned no certificate", host)
		}
		return certificate, nil
	}
	return config, nil
}

// CloseIdleConnections drops reusable upstream sockets after a route or daemon lifecycle change.
func (router *Router) CloseIdleConnections() {
	router.transport.CloseIdleConnections()
}

// DroppedErrors returns the number of diagnostics discarded while an observer was already running.
func (router *Router) DroppedErrors() uint64 {
	return router.dropped.Load()
}

// handleHTTP preserves path and query only after the authority belongs to the current snapshot.
func (router *Router) handleHTTP(writer http.ResponseWriter, request *http.Request) {
	if !validOriginRequest(request) {
		http.Error(writer, "Harbor accepts origin-form HTTP requests only.", http.StatusBadRequest)
		return
	}
	route, found := router.snapshot.Load().routeForAuthority(request.Host)
	if !found {
		http.Error(writer, "Harbor has no route for this host.", http.StatusMisdirectedRequest)
		return
	}
	target := url.URL{
		Scheme:   "https",
		Host:     route.Host,
		Path:     request.URL.Path,
		RawPath:  request.URL.RawPath,
		RawQuery: request.URL.RawQuery,
	}
	http.Redirect(writer, request, target.String(), http.StatusPermanentRedirect)
}

// handleHTTPS requires the HTTP authority and authenticated TLS name to select the same exact route.
func (router *Router) handleHTTPS(writer http.ResponseWriter, request *http.Request) {
	if !validOriginRequest(request) {
		http.Error(writer, "Harbor accepts origin-form HTTP requests only.", http.StatusBadRequest)
		return
	}
	if request.TLS == nil {
		http.Error(writer, "Harbor requires TLS for this route.", http.StatusMisdirectedRequest)
		return
	}
	snapshot := router.snapshot.Load()
	route, found := snapshot.routeForAuthority(request.Host)
	if !found {
		http.Error(writer, "Harbor has no route for this host.", http.StatusMisdirectedRequest)
		return
	}
	sni, err := canonicalDomain(request.TLS.ServerName)
	if err != nil || sni != route.Host {
		http.Error(writer, "Harbor requires matching Host and TLS names.", http.StatusMisdirectedRequest)
		return
	}
	if request.ContentLength > router.config.MaxRequestBodyBytes {
		http.Error(writer, "Harbor rejected a request body larger than its ingress limit.", http.StatusRequestEntityTooLarge)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, router.config.MaxRequestBodyBytes)
	request = request.WithContext(context.WithValue(request.Context(), routeContextKey{}, route))
	router.proxy.ServeHTTP(writer, request)
}

// rewrite applies only the route placed in context after the Host/SNI authorization boundary.
func (router *Router) rewrite(request *httputil.ProxyRequest) {
	route, ok := request.In.Context().Value(routeContextKey{}).(Route)
	if !ok {
		panic("httpingress: proxy request has no authorized route")
	}
	target := &url.URL{Scheme: "http", Host: route.Upstream.String()}
	request.SetURL(target)
	request.Out.Host = route.Host
	for _, header := range untrustedForwardingHeaders {
		request.Out.Header.Del(header)
	}
	request.SetXForwarded()
	if clientIP, _, err := net.SplitHostPort(request.In.RemoteAddr); err == nil {
		request.Out.Header.Set("X-Real-IP", clientIP)
	}
}

// handleProxyError returns a stable gateway failure while keeping upstream details out of the client response.
func (router *Router) handleProxyError(writer http.ResponseWriter, request *http.Request, err error) {
	router.observe(fmt.Errorf("proxy %q: %w", request.Host, err))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(writer, "Harbor rejected a request body larger than its ingress limit.", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(writer, "Harbor could not reach this project.", http.StatusBadGateway)
}

// validateUpstreamResponse rejects an impossible nil response before the proxy dereferences it.
func (router *Router) validateUpstreamResponse(response *http.Response) error {
	if response == nil {
		return errors.New("private upstream returned no HTTP response")
	}
	return nil
}

// observe schedules at most one callback so diagnostics cannot delay ingress or grow without bound.
func (router *Router) observe(err error) {
	if router.config.ObserveError == nil {
		return
	}
	select {
	case router.observer <- struct{}{}:
		go router.runObserver(err)
	default:
		router.dropped.Add(1)
	}
}

// runObserver contains callback panics and releases admission only after the callback returns.
func (router *Router) runObserver(err error) {
	defer func() {
		_ = recover()
		<-router.observer
	}()
	router.config.ObserveError(err)
}

// normalizeConfig gives zero values conservative production bounds.
func normalizeConfig(config Config) Config {
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = defaultConnectTimeout
	}
	if config.ResponseHeaderTimeout == 0 {
		config.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	}
	if config.IdleConnectionTimeout == 0 {
		config.IdleConnectionTimeout = defaultIdleConnectionTimeout
	}
	if config.MaxConnectionsPerHost == 0 {
		config.MaxConnectionsPerHost = defaultMaximumConnections
	}
	if config.MaxRequestBodyBytes == 0 {
		config.MaxRequestBodyBytes = defaultMaximumRequestBody
	}
	return config
}

// validateConfig rejects resource bounds that would disable protection or stall shutdown indefinitely.
func validateConfig(config Config) error {
	if config.ConnectTimeout < 0 || config.ConnectTimeout > maximumConnectTimeout {
		return fmt.Errorf("ingress connect timeout must be between zero and %s", maximumConnectTimeout)
	}
	if config.ResponseHeaderTimeout < 0 || config.ResponseHeaderTimeout > maximumResponseHeaderTimeout {
		return fmt.Errorf("ingress response header timeout must be between zero and %s", maximumResponseHeaderTimeout)
	}
	if config.IdleConnectionTimeout < 0 || config.IdleConnectionTimeout > maximumIdleConnectionTimeout {
		return fmt.Errorf("ingress idle connection timeout must be between zero and %s", maximumIdleConnectionTimeout)
	}
	if config.MaxConnectionsPerHost < 1 || config.MaxConnectionsPerHost > hardMaximumConnections {
		return fmt.Errorf("ingress maximum connections per host must be between 1 and %d", hardMaximumConnections)
	}
	if config.MaxRequestBodyBytes < 1 || config.MaxRequestBodyBytes > hardMaximumRequestBody {
		return fmt.Errorf("ingress maximum request body bytes must be between 1 and %d", hardMaximumRequestBody)
	}
	return nil
}

// validOriginRequest prevents Harbor from becoming an HTTP forward proxy or CONNECT tunnel.
func validOriginRequest(request *http.Request) bool {
	if request == nil || request.URL == nil || request.Method == http.MethodConnect {
		return false
	}
	return !request.URL.IsAbs() && request.RequestURI != "*"
}
