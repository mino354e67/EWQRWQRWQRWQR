package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

type Config struct {
	ListenAddr     string
	StateFilePath  string
	GatewayBaseURL string
	CooldownUSD    float64
}

type Key struct {
	Name            string    `json:"name"`
	Email           string    `json:"email"`
	APIKey          string    `json:"api_key"`
	MonthlySpentUSD float64   `json:"monthly_spent_usd"`
	MonthTag        string    `json:"month_tag"`
	Paused          bool      `json:"paused"`
	LastBalance     float64   `json:"last_balance"`
	LastUsedTotal   float64   `json:"last_used_total"`
	LastPollAt      time.Time `json:"last_poll_at"`
	LastErr         string    `json:"last_error"`
	LastProxyAt     time.Time `json:"last_proxy_at"`
	ProxyReqCount   int64     `json:"proxy_request_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	RequestCount    int64     `json:"request_count"`
	ID              string    `json:"id"`
}

type PublicKeyView struct {
	Name            string    `json:"name"`
	Email           string    `json:"email"`
	MonthlySpentUSD float64   `json:"monthly_spent_usd"`
	MonthTag        string    `json:"month_tag"`
	Paused          bool      `json:"paused"`
	LastBalance     float64   `json:"last_balance"`
	LastUsedTotal   float64   `json:"last_used_total"`
	LastPollAt      time.Time `json:"last_poll_at"`
	LastErr         string    `json:"last_error"`
	LastProxyAt     time.Time `json:"last_proxy_at"`
	ProxyReqCount   int64     `json:"proxy_request_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	RequestCount    int64     `json:"request_count"`
	ID              string    `json:"id"`
}

type AppState struct {
	mu        sync.RWMutex
	StateFile string          `json:"-"`
	Cooldown  float64         `json:"cooldown_usd"`
	LastSaved time.Time       `json:"last_saved"`
	Keys      map[string]*Key `json:"keys"`
}

type CreateKeyReq struct {
	Name   string `json:"name"`
	Email  string `json:"email"`
	APIKey string `json:"api_key"`
}

type UpdateKeyReq struct {
	Name      *string `json:"name,omitempty"`
	Email     *string `json:"email,omitempty"`
	Paused    *bool   `json:"paused,omitempty"`
	ResetCost bool    `json:"reset_cost,omitempty"`
}

type refreshReq struct {
	ID string `json:"id"`
}

type CreditsResp struct {
	Data struct {
		Balance   float64 `json:"balance"`
		TotalUsed float64 `json:"total_used"`
	} `json:"data"`
}

type proxyCandidate struct {
	ID     string
	APIKey string
}

type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.f.Flush()
	return n, err
}

var proxyHTTPClient *http.Client
var emailPattern *regexp.Regexp

func init() {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: 30 * time.Second,
	}
	proxyHTTPClient = &http.Client{Transport: tr}
	emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
}

func main() {
	cfg := readConfig()
	state, err := loadState(cfg.StateFilePath, cfg.CooldownUSD)
	if err != nil {
		log.Fatalf("load state: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/api/state", withCORS(handleGetState(state, cfg)))
	mux.HandleFunc("/api/refresh", withCORS(handleRefresh(state, cfg)))
	mux.HandleFunc("/api/keys", withCORS(handleKeys(state, cfg)))
	mux.HandleFunc("/api/keys/", withCORS(handleKeyByID(state, cfg)))
	mux.HandleFunc("/v1", withCORS(handleGatewayProxy(state, cfg)))
	mux.HandleFunc("/v1/", withCORS(handleGatewayProxy(state, cfg)))

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           logRequest(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("AI Gateway poller listening on http://localhost%s", cfg.ListenAddr)
	log.Printf("manual refresh mode cooldown=%.2fUSD gateway=%s", cfg.CooldownUSD, cfg.GatewayBaseURL)
	log.Fatal(srv.ListenAndServe())
}

func readConfig() Config {
	cfg := Config{
		ListenAddr:     getenvDefault("LISTEN_ADDR", ":9090"),
		StateFilePath:  filepath.Join(getenvDefault("STATE_DIR", "."), "state.json"),
		GatewayBaseURL: getenvDefault("GATEWAY_BASE_URL", "https://ai-gateway.vercel.sh/v1"),
		CooldownUSD:    5.0,
	}
	if v := os.Getenv("MONTHLY_COOLDOWN_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.CooldownUSD = f
		}
	}
	return cfg
}

func loadState(path string, cooldown float64) (*AppState, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	state := &AppState{
		StateFile: path,
		Cooldown:  cooldown,
		Keys:      make(map[string]*Key),
	}

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := state.save(); err != nil {
			return nil, err
		}
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return state, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, err
	}
	state.StateFile = path
	if state.Keys == nil {
		state.Keys = make(map[string]*Key)
	}
	if state.Cooldown <= 0 {
		state.Cooldown = cooldown
	}
	tag := currentMonthTag()
	for _, k := range state.Keys {
		if k.MonthTag == "" {
			k.MonthTag = tag
		} else if k.MonthTag != tag {
			k.MonthTag = tag
			k.MonthlySpentUSD = 0
			k.Paused = false
		}
	}
	return state, nil
}

func (s *AppState) save() error {
	s.LastSaved = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.StateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.StateFile)
}

func pollOne(ctx context.Context, state *AppState, cfg Config, id string) error {
	state.mu.Lock()
	k, ok := state.Keys[id]
	if !ok {
		state.mu.Unlock()
		return errors.New("key not found")
	}
	rollMonthIfNeeded(k, currentMonthTag())
	if k.Paused {
		state.mu.Unlock()
		return nil
	}
	apiKey := k.APIKey
	prevUsed := k.LastUsedTotal
	state.mu.Unlock()

	resp, err := fetchCredits(ctx, cfg.GatewayBaseURL, apiKey)
	now := time.Now()

	state.mu.Lock()
	defer state.mu.Unlock()
	k, ok = state.Keys[id]
	if !ok {
		return errors.New("key not found")
	}
	rollMonthIfNeeded(k, currentMonthTag())
	k.LastPollAt = now
	k.UpdatedAt = now
	if err != nil {
		k.LastErr = err.Error()
		_ = state.save()
		return err
	}
	k.RequestCount++
	k.LastErr = ""
	k.LastBalance = resp.Data.Balance
	k.LastUsedTotal = resp.Data.TotalUsed
	if resp.Data.TotalUsed > prevUsed {
		k.MonthlySpentUSD += resp.Data.TotalUsed - prevUsed
	}
	if k.MonthlySpentUSD >= state.Cooldown {
		k.Paused = true
	}
	return state.save()
}

func fetchCredits(ctx context.Context, baseURL, apiKey string) (*CreditsResp, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("empty api key")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/credits", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("credits status=%d body=%s", resp.StatusCode, string(body))
	}
	var out CreditsResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("upstream status=%d body=%s", resp.StatusCode, string(body))
	}
	return &out, nil
}

func handleGetState(state *AppState, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		state.mu.RLock()
		defer state.mu.RUnlock()
		list := make([]PublicKeyView, 0, len(state.Keys))
		for _, k := range state.Keys {
			list = append(list, publicView(k))
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"cooldown_usd": state.Cooldown,
			"month":        currentMonthTag(),
			"keys":         list,
		})
	}
}

func handleRefresh(state *AppState, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req refreshReq
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
		var ids []string
		state.mu.RLock()
		if req.ID != "" {
			if _, ok := state.Keys[req.ID]; ok {
				ids = append(ids, req.ID)
			}
		} else {
			ids = state.keyIDs()
		}
		state.mu.RUnlock()

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		errs := map[string]string{}
		refreshed := 0
		for _, id := range ids {
			if err := pollOne(ctx, state, cfg, id); err != nil {
				errs[id] = err.Error()
			} else {
				refreshed++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"refreshed": refreshed,
			"errors":    errs,
		})
	}
}

func handleKeys(state *AppState, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		var req CreateKeyReq
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		req.Email = strings.TrimSpace(req.Email)
		req.APIKey = strings.TrimSpace(req.APIKey)
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name cannot be empty"})
			return
		}
		if req.APIKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty api key"})
			return
		}
		if req.Email != "" && !emailPattern.MatchString(req.Email) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid email"})
			return
		}
		now := time.Now()
		k := &Key{
			ID:        newID(),
			Name:      req.Name,
			Email:     req.Email,
			APIKey:    req.APIKey,
			MonthTag:  currentMonthTag(),
			CreatedAt: now,
			UpdatedAt: now,
		}
		state.mu.Lock()
		state.Keys[k.ID] = k
		err = state.save()
		state.mu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": publicView(k)})
	}
}

func handleKeyByID(state *AppState, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/keys/")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
			return
		}
		switch r.Method {
		case http.MethodDelete:
			state.mu.Lock()
			if _, ok := state.Keys[id]; !ok {
				state.mu.Unlock()
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			delete(state.Keys, id)
			err := state.save()
			state.mu.Unlock()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		case http.MethodPatch:
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			var req UpdateKeyReq
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			state.mu.Lock()
			k, ok := state.Keys[id]
			if !ok {
				state.mu.Unlock()
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			if req.Name != nil {
				name := strings.TrimSpace(*req.Name)
				if name == "" {
					state.mu.Unlock()
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name cannot be empty"})
					return
				}
				k.Name = name
			}
			if req.Email != nil {
				email := strings.TrimSpace(*req.Email)
				if email != "" && !emailPattern.MatchString(email) {
					state.mu.Unlock()
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid email"})
					return
				}
				k.Email = email
			}
			if req.Paused != nil {
				k.Paused = *req.Paused
			}
			if req.ResetCost {
				k.MonthlySpentUSD = 0
				k.MonthTag = currentMonthTag()
				k.Paused = false
			}
			k.UpdatedAt = time.Now()
			err = state.save()
			state.mu.Unlock()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"key": publicView(k)})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleGatewayProxy(state *AppState, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		trimmed := strings.TrimPrefix(r.URL.Path, "/v1")
		suffix := trimmed
		if r.URL.RawQuery != "" {
			suffix = trimmed + "?" + r.URL.RawQuery
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body failed"})
			return
		}

		streamReq := requestWantsStream(body)

		candidates := state.nextProxyCandidates()
		if len(candidates) == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no available key"})
			return
		}

		for i, candidate := range candidates {
			ctx, cancel := context.WithCancel(context.Background())

			req, err := http.NewRequestWithContext(ctx, r.Method, cfg.GatewayBaseURL+suffix, bytes.NewReader(body))
			if err != nil {
				cancel()
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			copyProxyHeaders(req.Header, r.Header)
			req.Header.Set("Authorization", "Bearer "+candidate.APIKey)
			if streamReq {
				req.Header.Set("Accept", "text/event-stream")
			}
			req.Header.Set("Accept-Encoding", "identity")

			resp, err := proxyHTTPClient.Do(req)
			if err != nil {
				cancel()
				state.markProxyFailure(candidate.ID, err.Error())
				continue
			}

			if shouldRetryWithNextKey(resp.StatusCode) && i < len(candidates)-1 {
				snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				cancel()
				state.markProxyFailure(candidate.ID, fmt.Sprintf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(snippet))))
				continue
			}

			ct := resp.Header.Get("Content-Type")
			isSSE := strings.Contains(strings.ToLower(ct), "text/event-stream")

			copyResponseHeaders(w.Header(), resp.Header)
			if streamReq && isSSE {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Set("X-Accel-Buffering", "no")
			}
			w.WriteHeader(resp.StatusCode)

			var copyErr error
			if streamReq && isSSE {
				_, copyErr = streamCopy(w, resp.Body)
			} else {
				_, copyErr = io.Copy(w, resp.Body)
			}
			resp.Body.Close()
			cancel()

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				state.markProxySuccess(candidate.ID)
			} else {
				state.markProxyFailure(candidate.ID, fmt.Sprintf("upstream status=%d", resp.StatusCode))
			}

			log.Printf("proxy upstream status=%d stream_req=%t sse=%t ct=%s path=%s", resp.StatusCode, streamReq, isSSE, ct, suffix)
			if copyErr != nil {
				log.Printf("proxy stream mode key=%s path=%s err=%v", candidate.ID, suffix, copyErr)
			}
			return
		}

		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "all keys failed"})
	}
}

func (s *AppState) nextProxyCandidates() []proxyCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]proxyCandidate, 0, len(s.Keys))
	tag := currentMonthTag()
	for _, k := range s.Keys {
		rollMonthIfNeeded(k, tag)
		if k.Paused {
			continue
		}
		if strings.TrimSpace(k.APIKey) == "" {
			continue
		}
		out = append(out, proxyCandidate{ID: k.ID, APIKey: k.APIKey})
	}
	sort.Slice(out, func(i, j int) bool {
		ki := s.Keys[out[i].ID]
		kj := s.Keys[out[j].ID]
		return ki.MonthlySpentUSD < kj.MonthlySpentUSD
	})
	return out
}

func (s *AppState) markProxySuccess(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.Keys[id]
	if !ok {
		return
	}
	now := time.Now()
	k.LastProxyAt = now
	k.ProxyReqCount++
	k.UpdatedAt = now
	k.LastErr = ""
	_ = s.save()
}

func (s *AppState) markProxyFailure(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.Keys[id]
	if !ok {
		return
	}
	now := time.Now()
	k.LastProxyAt = now
	k.UpdatedAt = now
	k.LastErr = msg
	_ = s.save()
}

func shouldRetryWithNextKey(status int) bool {
	return status == http.StatusUnauthorized ||
		status == http.StatusForbidden ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

func copyProxyHeaders(dst, src http.Header) {
	for k, vv := range src {
		lk := strings.ToLower(k)
		switch lk {
		case "authorization", "host", "connection", "proxy-connection", "keep-alive", "upgrade", "transfer-encoding":
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vv := range src {
		lk := strings.ToLower(k)
		switch lk {
		case "connection", "proxy-connection", "keep-alive", "upgrade", "transfer-encoding", "content-length":
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func streamCopy(w http.ResponseWriter, src io.Reader) (int64, error) {
	if f, ok := w.(http.Flusher); ok {
		fw := &flushWriter{w: w, f: f}
		return io.Copy(fw, src)
	}
	return io.Copy(w, src)
}

func requestWantsStream(body []byte) bool {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	v, ok := m["stream"]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func publicView(k *Key) PublicKeyView {
	return PublicKeyView{
		ID:              k.ID,
		Name:            k.Name,
		Email:           k.Email,
		MonthlySpentUSD: roundCents(k.MonthlySpentUSD),
		MonthTag:        k.MonthTag,
		Paused:          k.Paused,
		LastBalance:     roundCents(k.LastBalance),
		LastUsedTotal:   roundCents(k.LastUsedTotal),
		LastPollAt:      k.LastPollAt,
		LastErr:         k.LastErr,
		LastProxyAt:     k.LastProxyAt,
		ProxyReqCount:   k.ProxyReqCount,
		CreatedAt:       k.CreatedAt,
		UpdatedAt:       k.UpdatedAt,
		RequestCount:    k.RequestCount,
	}
}

func rollMonthIfNeeded(k *Key, tag string) {
	if k.MonthTag == "" {
		k.MonthTag = tag
		return
	}
	if k.MonthTag != tag {
		k.MonthTag = tag
		k.MonthlySpentUSD = 0
		k.Paused = false
	}
}

func (s *AppState) keyIDs() []string {
	out := make([]string, 0, len(s.Keys))
	for id := range s.Keys {
		out = append(out, id)
	}
	return out
}

func currentMonthTag() string {
	return time.Now().Format("2006-01")
}

func roundCents(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		h(w, r)
	}
}

func logRequest(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}
