package registry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const PublicCatalogURL = "https://models.dev/api.json"
const catalogLimit = 16 << 20

type CatalogProvider struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Models int    `json:"models"`
}
type CatalogStatus struct {
	Source       string            `json:"source"`
	State        string            `json:"state"`
	RetrievedAt  string            `json:"retrievedAt"`
	CheckedAt    string            `json:"checkedAt"`
	ETag         string            `json:"etag"`
	LastModified string            `json:"lastModified"`
	SHA256       string            `json:"sha256"`
	Bytes        int               `json:"bytes"`
	Providers    []CatalogProvider `json:"providers"`
	LastError    string            `json:"lastError"`
}

type cachedCatalog struct {
	status    CatalogStatus
	providers map[string]modelsDevProvider
	raw       []byte
}

func catalogHTTPClient() *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second, MaxIdleConns: 1, IdleConnTimeout: 15 * time.Second}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if host != "models.dev" {
			return nil, fmt.Errorf("catalog host denied")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsUnspecified() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsMulticast() {
				return nil, fmt.Errorf("catalog address denied")
			}
		}
		var last error
		for _, ip := range ips {
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("catalog DNS has no addresses")
		}
		return nil, last
	}
	return &http.Client{Timeout: 12 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("catalog redirects denied") }}
}

func (s *Service) cachedCatalog(ctx context.Context) (cachedCatalog, error) {
	out := cachedCatalog{status: CatalogStatus{Source: PublicCatalogURL, State: "EMPTY", Providers: []CatalogProvider{}}}
	err := s.db.QueryRowContext(ctx, `SELECT raw, retrieved_at,checked_at,etag,last_modified,sha256,last_error FROM registry_catalog WHERE id=1`).Scan(&out.raw, &out.status.RetrievedAt, &out.status.CheckedAt, &out.status.ETag, &out.status.LastModified, &out.status.SHA256, &out.status.LastError)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if len(out.raw) > 0 {
		if err := json.Unmarshal(out.raw, &out.providers); err != nil {
			return out, err
		}
		out.status.State = "CACHED"
		out.status.Bytes = len(out.raw)
		for id, p := range out.providers {
			out.status.Providers = append(out.status.Providers, CatalogProvider{ID: id, Name: p.Name, Models: len(p.Models)})
		}
		sort.Slice(out.status.Providers, func(i, j int) bool { return out.status.Providers[i].ID < out.status.Providers[j].ID })
	}
	return out, nil
}

func (s *Service) RefreshCatalog(ctx context.Context) (CatalogStatus, error) {
	client := catalogHTTPClient()
	defer client.CloseIdleConnections()
	return s.refreshCatalog(ctx, client, PublicCatalogURL)
}

func (s *Service) refreshCatalog(ctx context.Context, client *http.Client, source string) (status CatalogStatus, resultErr error) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	cached, err := s.cachedCatalog(ctx)
	if err != nil {
		return status, err
	}
	checked := s.now().UTC().Format(time.RFC3339Nano)
	defer func() {
		if resultErr != nil {
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			s.db.ExecContext(recordCtx, `UPDATE registry_catalog SET checked_at=?,last_error=? WHERE id=1`, checked, "REFRESH_FAILED_OLD_SNAPSHOT_RETAINED")
			status = cached.status
			status.CheckedAt = checked
			status.State = "FAILED_OLD_SNAPSHOT_RETAINED"
			status.LastError = "REFRESH_FAILED_OLD_SNAPSHOT_RETAINED"
		}
	}()
	req, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return status, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Enhanced-Kimi-Desktop/0.1.1 (public metadata only)")
	if cached.status.ETag != "" {
		req.Header.Set("If-None-Match", cached.status.ETag)
	}
	if cached.status.LastModified != "" {
		req.Header.Set("If-Modified-Since", cached.status.LastModified)
	}
	resp, err := client.Do(req)
	if err != nil {
		return status, fmt.Errorf("public catalog fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 304 && len(cached.raw) > 0 {
		_, err = s.db.ExecContext(ctx, `UPDATE registry_catalog SET checked_at=?,last_error='' WHERE id=1`, checked)
		status = cached.status
		status.CheckedAt = checked
		status.State = "NOT_MODIFIED"
		status.LastError = ""
		return status, err
	}
	if resp.StatusCode != 200 {
		return status, fmt.Errorf("public catalog HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, catalogLimit+1))
	if err != nil {
		return status, err
	}
	if len(raw) > catalogLimit {
		return status, fmt.Errorf("public catalog exceeds size limit")
	}
	var providers map[string]modelsDevProvider
	if err = json.Unmarshal(raw, &providers); err != nil || len(providers) == 0 {
		return status, fmt.Errorf("public catalog invalid or empty")
	}
	count := 0
	for id, p := range providers {
		if !providerIDPattern.MatchString(id) || (p.ID != "" && p.ID != id) {
			return status, fmt.Errorf("catalog provider identity invalid")
		}
		count += len(p.Models)
	}
	if count == 0 {
		return status, fmt.Errorf("public catalog contains no models")
	}
	sum := sha256.Sum256(raw)
	_, err = s.db.ExecContext(ctx, `INSERT INTO registry_catalog(id,raw,retrieved_at,checked_at,etag,last_modified,sha256,last_error) VALUES(1,?,?,?,?,?,?,'') ON CONFLICT(id) DO UPDATE SET raw=excluded.raw,retrieved_at=excluded.retrieved_at,checked_at=excluded.checked_at,etag=excluded.etag,last_modified=excluded.last_modified,sha256=excluded.sha256,last_error=''`, raw, checked, checked, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), hex.EncodeToString(sum[:]))
	if err != nil {
		return status, err
	}
	cached, err = s.cachedCatalog(ctx)
	status = cached.status
	status.State = "FETCHED"
	return status, err
}

// FindPublicModel matches an official provider endpoint and model ID to the public catalog.
func (s *Service) FindPublicModel(ctx context.Context, providerID, baseURL, modelID string) (map[string]Capability, error) {
	if modelID == "" {
		return nil, nil
	}
	found, err := s.FindPublicModels(ctx, providerID, baseURL, []string{modelID})
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return found[modelID], nil
}

// FindPublicModels matches a batch of model IDs for one official provider endpoint against a single cached catalog read.
func (s *Service) FindPublicModels(ctx context.Context, providerID, baseURL string, modelIDs []string) (map[string]map[string]Capability, error) {
	endpoint := publicCatalogEndpoint(baseURL)
	if endpoint == "" || len(modelIDs) == 0 {
		return nil, nil
	}
	uniqueIDs := make([]string, 0, len(modelIDs))
	seen := make(map[string]bool, len(modelIDs))
	for _, id := range modelIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return nil, nil
	}
	cached, err := s.cachedCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if len(cached.raw) == 0 {
		if _, err := s.RefreshCatalog(ctx); err != nil {
			return nil, err
		}
		cached, err = s.cachedCatalog(ctx)
		if err != nil {
			return nil, err
		}
	}

	result := make(map[string]map[string]Capability, len(uniqueIDs))
	for _, modelID := range uniqueIDs {
		caps, err := matchPublicModel(cached.providers, cached.status.RetrievedAt, providerID, endpoint, modelID)
		if err != nil {
			return nil, err
		}
		if len(caps) > 0 {
			result[modelID] = caps
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func matchPublicModel(providers map[string]modelsDevProvider, retrievedAt, providerID, endpoint, modelID string) (map[string]Capability, error) {
	matches := make(map[string]modelsDevProvider)
	for id, provider := range providers {
		model, ok := provider.Models[modelID]
		if !ok || (model.ID != "" && model.ID != modelID) || catalogProviderEndpoint(id, provider.API) != endpoint {
			continue
		}
		matches[id] = modelsDevProvider{ID: provider.ID, Models: map[string]modelsDevModel{modelID: model}}
	}
	if exact, ok := matches[providerID]; ok {
		matches = map[string]modelsDevProvider{providerID: exact}
	}
	if len(matches) != 1 {
		return nil, nil
	}
	models, err := parseModelsDev(ModelsDevCatalog{Source: PublicCatalogURL, RetrievedAt: retrievedAt, Providers: matches})
	if err != nil {
		return nil, err
	}
	return models[0].Capabilities, nil
}

func catalogProviderEndpoint(providerID, raw string) string {
	if raw == "" {
		for _, preset := range providerPresets {
			if preset.Official && preset.ID == providerID {
				raw = preset.BaseURL
				break
			}
		}
	}
	return publicCatalogEndpoint(raw)
}

func publicCatalogEndpoint(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	if u.Port() == "443" {
		host = strings.TrimSuffix(host, ":443")
	}
	return "https://" + host + strings.TrimRight(u.EscapedPath(), "/")
}

func (s *Service) catalogInputs(ctx context.Context, sourceProvider, targetProvider string) ([]modelInput, error) {
	if !providerIDPattern.MatchString(targetProvider) {
		return nil, badRequest("INVALID_PROVIDER_ID", "目标供应商 ID 无效")
	}
	exists, err := s.providerExists(s.db, targetProvider)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, notFound("PROVIDER_NOT_FOUND", "先创建本地供应商实例")
	}
	cached, err := s.cachedCatalog(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := cached.providers[sourceProvider]
	if !ok {
		return nil, notFound("CATALOG_PROVIDER_NOT_FOUND", "缓存没有该公共供应商")
	}
	records, err := parseModelsDev(ModelsDevCatalog{Source: PublicCatalogURL, RetrievedAt: cached.status.RetrievedAt, Providers: map[string]modelsDevProvider{sourceProvider: p}})
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].ProviderID = targetProvider
		for f, c := range records[i].Capabilities {
			revision := cached.status.SHA256
			c.SourceRevision = &revision
			records[i].Capabilities[f] = c
		}
	}
	return records, nil
}
