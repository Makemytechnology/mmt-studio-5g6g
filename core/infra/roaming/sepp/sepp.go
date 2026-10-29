// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package sepp — Security Edge Protection Proxy (TS 29.573).
//
// Go port of infra/roaming/sepp/sepp_server.py.
//
// N32 interface between PLMNs:
//   - N32-c: Control plane — TLS handshake, capability negotiation
//   - N32-f: Forwarding — HTTP reverse proxy with message filtering
//
// SEPP sits at the PLMN border. All inter-PLMN SBI messages pass through it.
// Provides: TLS termination, message filtering, topology hiding.
package sepp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("sepp")

// Server is the SEPP N32-f forwarding proxy.
type Server struct {
	Host     string
	Port     int
	CertFile string
	KeyFile  string

	mu     sync.Mutex
	server *http.Server
}

// New creates a SEPP server with the given configuration.
func New(host string, port int, certFile, keyFile string) *Server {
	return &Server{
		Host:     host,
		Port:     port,
		CertFile: certFile,
		KeyFile:  keyFile,
	}
}

// Start launches the SEPP proxy in a background goroutine.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.proxyHandler)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("sepp listen: %w", err)
	}

	// TLS for N32-c (TS 29.573 §5.2)
	if s.CertFile != "" && s.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			ln.Close()
			return fmt.Errorf("sepp tls: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		ln = tls.NewListener(ln, tlsCfg)
		log.Info("SEPP started (TLS)", "addr", addr)
	} else {
		log.Info("SEPP started (no TLS — dev mode)", "addr", addr)
	}

	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("SEPP serve error", "err", err)
		}
	}()
	return nil
}

// Stop gracefully shuts down the SEPP server.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.server.Shutdown(ctx)
		log.Info("SEPP stopped")
		s.server = nil
	}
}

// proxyHandler is the N32-f forwarding proxy handler (TS 29.573 §5.3).
// Routes via the 3gpp-Sbi-Target-apiRoot header.
func (s *Server) proxyHandler(w http.ResponseWriter, r *http.Request) {
	targetRoot := r.Header.Get("3gpp-Sbi-Target-apiRoot")
	if targetRoot == "" {
		sendError(w, http.StatusBadRequest, "Missing 3gpp-Sbi-Target-apiRoot header")
		return
	}

	targetURL := strings.TrimRight(targetRoot, "/") + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Read body
	var body io.Reader
	if r.Body != nil {
		body = r.Body
		defer r.Body.Close()
	}

	log.Debug("SEPP N32-f proxy", "method", r.Method, "path", r.URL.Path, "target", targetURL)

	// Build forwarded request — filter sensitive headers
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, body)
	if err != nil {
		sendError(w, http.StatusBadGateway, fmt.Sprintf("SEPP proxy error: %v", err))
		return
	}
	for k, vv := range r.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "content-length" || lk == "3gpp-sbi-target-apiroot" {
			continue
		}
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		proxyReq.Header.Set("Content-Type", ct)
	} else {
		proxyReq.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		sendError(w, http.StatusBadGateway, fmt.Sprintf("SEPP proxy error: %v", err))
		return
	}
	defer resp.Body.Close()

	// Copy response headers (filter hop-by-hop)
	for k, vv := range resp.Header {
		lk := strings.ToLower(k)
		if lk == "transfer-encoding" || lk == "connection" {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// sendError writes a JSON error response.
func sendError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ── Module-level convenience ──

var (
	globalMu   sync.Mutex
	globalSEPP *Server
)

// StartSEPP creates and starts the global SEPP instance.
func StartSEPP(host string, port int, certFile, keyFile string) (*Server, error) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalSEPP = New(host, port, certFile, keyFile)
	if err := globalSEPP.Start(); err != nil {
		return nil, err
	}
	return globalSEPP, nil
}

// StopSEPP stops the global SEPP instance.
func StopSEPP() {
	globalMu.Lock()
	defer globalMu.Unlock()
	if globalSEPP != nil {
		globalSEPP.Stop()
		globalSEPP = nil
	}
}

// Status returns the current SEPP status.
func Status() map[string]any {
	globalMu.Lock()
	defer globalMu.Unlock()
	if globalSEPP != nil && globalSEPP.server != nil {
		return map[string]any{
			"status": "running",
			"addr":   fmt.Sprintf("%s:%d", globalSEPP.Host, globalSEPP.Port),
			"tls":    globalSEPP.CertFile != "",
		}
	}
	return map[string]any{"status": "stopped"}
}
