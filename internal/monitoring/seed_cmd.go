package monitoring

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"github.com/goforj/harbor/internal/console"
	"github.com/goforj/str/v2"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SeedCmd seeds monitoring targets for load testing.
type SeedCmd struct {
	Source            string `help:"Seed source (tranco, popular, file path, CSV URL, or comma-separated domains)" default:"tranco"`
	Profile           string `help:"Seed profile. Overrides source/count defaults when set." enum:"popular,top-1k,1k,top1k,top-10k,10k,top10k" default:"popular"`
	Count             int    `help:"Number of monitors to seed" default:"10000"`
	HostLimit         int    `help:"Maximum candidate hosts to resolve and validate before filtering. Defaults to 3x count when unset." default:"0"`
	Interval          int    `help:"Monitor interval in seconds" default:"60"`
	Timeout           int    `help:"Monitor timeout in milliseconds" default:"5000"`
	Tag               string `help:"Optional tag label for seeded monitors" default:"loadtest"`
	Replace           bool   `help:"Replace existing HTTP monitors that match the resolved seed targets"`
	CleanupBlocked    bool   `help:"Delete existing blocked adult/gambling HTTP monitors before seeding"`
	CleanupOnly       bool   `help:"Delete existing blocked adult/gambling HTTP monitors and exit"`
	Validate          bool   `help:"Validate targets are reachable before insert" default:"true"`
	ValidateTimeoutMS int    `help:"Validation timeout in milliseconds" default:"500"`
	ValidateWorkers   int    `help:"Concurrent workers for target validation" default:"100"`
	DryRun            bool   `help:"Preview monitors without writing to the database"`
}

const monitorSeedDefaultCount = 10000

// Signature defines CLI metadata for this command.
func (*SeedCmd) Signature() string {
	return `name:"monitor:seed" help:"Seed monitoring targets for load testing"`
}

// NewSeedCmd creates a new SeedCmd.
func NewSeedCmd() *SeedCmd {
	return &SeedCmd{}
}

// Run executes the command.
func (c *SeedCmd) Run(ctx context.Context) error {
	console.Warnf("monitor:seed is available only when Demo App and Database components are enabled")
	return nil
}

func seedValidationWorkerCount(requested int, total int) int {
	if requested <= 0 {
		requested = 1
	}
	if total <= 0 {
		return 1
	}
	if requested > total {
		return total
	}
	return requested
}

type blockedSeedMonitor struct {
	ID     int64
	Name   string
	Target string
	Reason string
}

func blockedSeedMonitorPreview(rows []blockedSeedMonitor, limit int) string {
	if len(rows) == 0 {
		return "none"
	}
	if limit <= 0 {
		limit = len(rows)
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		label := rows[i].Target
		if rows[i].Name != "" {
			label = rows[i].Name + " (" + rows[i].Target + ")"
		}
		if rows[i].Reason != "" {
			label += " [" + rows[i].Reason + "]"
		}
		out = append(out, label)
	}
	if len(rows) > limit {
		out = append(out, fmt.Sprintf("+%d more", len(rows)-limit))
	}
	return strings.Join(out, ", ")
}

func (c *SeedCmd) resolveHostLimit(count int) int {
	if c.HostLimit > 0 {
		return c.HostLimit
	}
	return count * 3
}

func validateTargetsConcurrently(
	targets []string,
	timeout time.Duration,
	workers int,
	onProgress func(processed, accepted, skippedUnreachable int),
) ([]bool, int, int) {
	total := len(targets)
	if total == 0 {
		return nil, 0, 0
	}
	workers = seedValidationWorkerCount(workers, total)

	type result struct {
		index int
		ok    bool
	}

	jobs := make(chan int, workers)
	results := make(chan result, workers)
	var workerWG sync.WaitGroup

	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for idx := range jobs {
				ok := checkTargetReachable(targets[idx], timeout)
				results <- result{index: idx, ok: ok}
			}
		}()
	}
	go func() {
		for idx := range targets {
			jobs <- idx
		}
		close(jobs)
		workerWG.Wait()
		close(results)
	}()

	acceptedByIndex := make([]bool, total)
	processed := 0
	accepted := 0
	skipped := 0
	for item := range results {
		processed++
		if item.ok {
			acceptedByIndex[item.index] = true
			accepted++
		} else {
			skipped++
		}
		if onProgress != nil {
			onProgress(processed, accepted, skipped)
		}
	}
	return acceptedByIndex, accepted, skipped
}

func checkTargetReachable(target string, timeout time.Duration) bool {
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DisableKeepAlives: true,
		ForceAttemptHTTP2: false,
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
	getReq, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	getReq.Header.Set("User-Agent", "goforj-monitor-seed/1.0")
	getReq.Header.Set("Range", "bytes=0-0")
	getRes, getErr := client.Do(getReq)
	if getErr != nil {
		return false
	}
	_, _ = io.CopyN(io.Discard, getRes.Body, 1024)
	_ = getRes.Body.Close()
	return getRes.StatusCode > 0 && getRes.StatusCode < 500
}

func resolveSeedProfile(profile string, requestedCount int) (string, int, error) {
	p := str.Of(profile).Trim().ToLower().String()
	switch p {
	case "popular", "brands":
		// Popular profile is a curated fixed-size set by default.
		if requestedCount == monitorSeedDefaultCount {
			return "popular", len(monitorSeedPopularDomains), nil
		}
		return "popular", requestedCount, nil
	case "top-1k", "top1k", "1k":
		if requestedCount == monitorSeedDefaultCount {
			return "tranco", 1000, nil
		}
		return "tranco", requestedCount, nil
	case "top-10k", "top10k", "10k":
		if requestedCount == monitorSeedDefaultCount {
			return "tranco", 10000, nil
		}
		return "tranco", requestedCount, nil
	default:
		return "", 0, fmt.Errorf("unsupported profile %q (use popular, top-1k, or top-10k)", profile)
	}
}

var monitorSeedPopularDomains = []string{
	"google.com", "youtube.com", "facebook.com", "instagram.com", "x.com", "linkedin.com", "reddit.com",
	"github.com", "gitlab.com", "bitbucket.org", "stackoverflow.com", "npmjs.com", "pypi.org", "rubygems.org",
	"microsoft.com", "apple.com", "amazon.com", "aws.amazon.com", "cloudflare.com", "openai.com", "anthropic.com",
	"stripe.com", "shopify.com", "paypal.com", "adobe.com", "notion.so", "slack.com", "zoom.us", "dropbox.com",
	"atlassian.com", "digitalocean.com", "vercel.com", "netlify.com", "render.com", "heroku.com", "fly.io",
	"wikipedia.org", "mozilla.org", "nytimes.com", "bbc.com", "cnn.com", "imdb.com", "spotify.com", "netflix.com",
	"twitch.tv", "discord.com", "whatsapp.com", "telegram.org", "tiktok.com", "airbnb.com", "uber.com", "doordash.com",
}

var monitorSeedCanonicalTargets = map[string]string{
	"google.com":         "https://www.google.com",
	"www.google.com":     "https://www.google.com",
	"cloudflare.com":     "https://www.cloudflare.com",
	"www.cloudflare.com": "https://www.cloudflare.com",
	"github.com":         "https://github.com",
	"www.github.com":     "https://github.com",
	"wikipedia.org":      "https://www.wikipedia.org",
	"www.wikipedia.org":  "https://www.wikipedia.org",
	"npmjs.com":          "https://registry.npmjs.org/-/ping",
	"www.npmjs.com":      "https://registry.npmjs.org/-/ping",
	"registry.npmjs.org": "https://registry.npmjs.org/-/ping",
	"proxy.golang.org":   "https://proxy.golang.org",
}

var monitorSeedCanonicalNames = map[string]string{
	"https://www.google.com":            "Google",
	"https://www.cloudflare.com":        "Cloudflare",
	"https://github.com":                "GitHub",
	"https://www.wikipedia.org":         "Wikipedia",
	"https://registry.npmjs.org/-/ping": "NPM Registry",
	"https://proxy.golang.org":          "Go Proxy",
}

var monitorSeedAcronymNames = map[string]string{
	"ai":    "AI",
	"api":   "API",
	"aws":   "AWS",
	"bbc":   "BBC",
	"cdn":   "CDN",
	"cnn":   "CNN",
	"dns":   "DNS",
	"http":  "HTTP",
	"https": "HTTPS",
	"id":    "ID",
	"io":    "IO",
	"ip":    "IP",
	"tls":   "TLS",
	"ui":    "UI",
	"url":   "URL",
	"ws":    "WS",
}

var monitorSeedFallbackDomains = []string{
	"google.com",
	"github.com",
	"cloudflare.com",
	"wikipedia.org",
	"npmjs.org",
	"proxy.golang.org",
	"golang.org",
	"mozilla.org",
	"microsoft.com",
	"apple.com",
	"openai.com",
	"stackoverflow.com",
}

var monitorSeedPublicCSVSources = []string{
	"https://tranco-list.eu/download/3Q95L/1000000",
	"https://tranco-list.eu/top-1m.csv.zip",
	"https://raw.githubusercontent.com/majesticmillion/majestic_million/master/majestic_million.csv",
}

var monitorSeedBlockedTLDs = map[string]string{
	"xxx":   "adult",
	"sex":   "adult",
	"porn":  "adult",
	"adult": "adult",
}

var monitorSeedBlockedExactLabels = map[string]string{
	"pornhub":       "adult",
	"spankbang":     "adult",
	"rule34":        "adult",
	"escort":        "adult",
	"escortbabylon": "adult",
	"redwap":        "adult",
	"nhentai":       "adult",
	"imhentai":      "adult",
	"ehentai":       "adult",
	"pussyboy":      "adult",
}

var monitorSeedBlockedLabelPrefixes = map[string]string{
	"porn":   "adult",
	"hentai": "adult",
	"sex":    "adult",
	"escort": "adult",
	"spank":  "adult",
	"casino": "gambling",
	"bet":    "gambling",
	"poker":  "gambling",
}

var monitorSeedBlockedLabelSuffixes = map[string]string{
	"porn":   "adult",
	"sex":    "adult",
	"hentai": "adult",
	"escort": "adult",
	"bet":    "gambling",
	"bets":   "gambling",
	"casino": "gambling",
	"poker":  "gambling",
}

var monitorSeedBlockedLabelContains = map[string]string{
	"porn":       "adult",
	"hentai":     "adult",
	"escort":     "adult",
	"spank":      "adult",
	"pussy":      "adult",
	"rule34":     "adult",
	"sexstories": "adult",
	"sexstory":   "adult",
	"sexzone":    "adult",
	"sexvid":     "adult",
	"sexvideo":   "adult",
	"sexdep":     "adult",
	"sexlog":     "adult",
	"sextop":     "adult",
	"sexkahani":  "adult",
	"bluefilm":   "adult",
}

func (c *SeedCmd) resolveDomains(ctx context.Context, source string, count int) ([]string, string, error) {
	raw := str.Of(source).Trim().String()
	if strings.EqualFold(raw, "popular") || strings.EqualFold(raw, "brands") {
		out := make([]string, 0, count)
		for len(out) < count {
			for _, domain := range monitorSeedPopularDomains {
				out = append(out, domain)
				if len(out) >= count {
					break
				}
			}
		}
		return out, "popular", nil
	}
	if raw == "" || strings.EqualFold(raw, "tranco") {
		domains, sourceName, err := fetchPublicDomains(ctx, count)
		if err == nil && len(domains) > 0 {
			return domains, sourceName, nil
		}
		console.Warnf("public domain list fetch failed, falling back to built-in domain set: %v", err)
		return fallbackDomains(count), "fallback", nil
	}

	lower := str.Of(raw).ToLower().String()
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		domains, err := fetchCSVSource(ctx, raw, count)
		if err != nil {
			return nil, "", err
		}
		return domains, raw, nil
	}

	if strings.Contains(raw, ",") {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			item := str.Of(part).Trim().String()
			if item != "" {
				out = append(out, item)
			}
		}
		return out, "inline", nil
	}

	if stat, err := os.Stat(raw); err == nil && !stat.IsDir() {
		domains, err := readDomainsFromFile(raw, count)
		if err != nil {
			return nil, "", err
		}
		return domains, filepath.Base(raw), nil
	}

	return nil, "", fmt.Errorf("unsupported source %q (use tranco, CSV URL, file path, or comma-separated domains)", source)
}

func fetchTrancoDomains(ctx context.Context, limit int) ([]string, error) {
	return fetchCSVSource(ctx, "https://tranco-list.eu/download/3Q95L/1000000", limit)
}

func fetchPublicDomains(ctx context.Context, limit int) ([]string, string, error) {
	var lastErr error
	for _, source := range monitorSeedPublicCSVSources {
		domains, err := fetchCSVSource(ctx, source, limit)
		if err != nil {
			lastErr = err
			continue
		}
		if len(domains) > 0 {
			return domains, source, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no domains returned from public sources")
	}
	return nil, "", lastErr
}

func fetchCSVSource(ctx context.Context, sourceURL string, limit int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("seed source request failed: %s", res.Status)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return parseCSVOrZip(body, sourceURL, limit)
}

func parseCSVOrZip(content []byte, sourceURL string, limit int) ([]string, error) {
	lowerURL := str.Of(sourceURL).Trim().ToLower().String()
	if strings.HasSuffix(lowerURL, ".zip") || looksLikeZip(content) {
		return parseCSVFromZip(content, limit)
	}
	return parseCSVRecords(csv.NewReader(bytes.NewReader(content)), limit)
}

func looksLikeZip(content []byte) bool {
	return len(content) >= 4 &&
		content[0] == 0x50 &&
		content[1] == 0x4b &&
		content[2] == 0x03 &&
		content[3] == 0x04
}

func parseCSVFromZip(content []byte, limit int) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, err
	}
	for _, file := range zr.File {
		name := str.Of(file.Name).Trim().ToLower().String()
		if !strings.HasSuffix(name, ".csv") {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		reader := csv.NewReader(rc)
		reader.FieldsPerRecord = -1
		out, parseErr := parseCSVRecords(reader, limit)
		closeErr := rc.Close()
		if parseErr != nil {
			return nil, parseErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("no csv content found in zip source")
}

func parseCSVRecords(reader *csv.Reader, limit int) ([]string, error) {
	reader.FieldsPerRecord = -1
	out := make([]string, 0, limit)
	for len(out) < limit {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		domain := parseDomainFromRecord(record)
		if domain != "" {
			out = append(out, domain)
		}
	}
	return out, nil
}

func readDomainsFromFile(path string, limit int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	out := make([]string, 0, limit)
	for len(out) < limit {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		domain := parseDomainFromRecord(record)
		if domain != "" {
			out = append(out, domain)
		}
	}
	return out, nil
}

func parseDomainFromRecord(record []string) string {
	if len(record) == 0 {
		return ""
	}
	for _, item := range record {
		part := str.Of(item).Trim().ToLower().String()
		if isLikelyDomain(part) {
			return part
		}
	}
	for _, item := range record {
		part := str.Of(item).Trim().String()
		if part == "" {
			continue
		}
		if _, err := strconv.Atoi(part); err == nil {
			continue
		}
		return part
	}
	return ""
}

func isLikelyDomain(value string) bool {
	if value == "" {
		return false
	}
	if strings.Contains(value, "://") {
		return false
	}
	if strings.Contains(value, "/") {
		return false
	}
	if strings.Contains(value, " ") {
		return false
	}
	if !strings.Contains(value, ".") {
		return false
	}
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	return true
}

func blockedSeedTarget(raw string) (bool, string) {
	host := seedTargetHostname(raw)
	if host == "" {
		return false, ""
	}
	labels := strings.Split(host, ".")
	if len(labels) == 0 {
		return false, ""
	}
	tld := seedNormalizedLabel(labels[len(labels)-1])
	if reason, blocked := monitorSeedBlockedTLDs[tld]; blocked {
		return true, reason
	}
	for _, label := range labels {
		normalized := seedNormalizedLabel(label)
		if normalized == "" {
			continue
		}
		if reason, blocked := monitorSeedBlockedExactLabels[normalized]; blocked {
			return true, reason
		}
		for prefix, reason := range monitorSeedBlockedLabelPrefixes {
			if strings.HasPrefix(normalized, prefix) {
				return true, reason
			}
		}
		for suffix, reason := range monitorSeedBlockedLabelSuffixes {
			if strings.HasSuffix(normalized, suffix) {
				return true, reason
			}
		}
		for needle, reason := range monitorSeedBlockedLabelContains {
			if strings.Contains(normalized, needle) {
				return true, reason
			}
		}
	}
	return false, ""
}

func seedTargetHostname(raw string) string {
	trimmed := str.Of(raw).Trim().ToLower().String()
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	return str.Of(parsed.Hostname()).Trim().ToLower().String()
}

func seedNormalizedLabel(raw string) string {
	raw = str.Of(raw).Trim().ToLower().String()
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fallbackDomains(count int) []string {
	if count <= 0 {
		return nil
	}
	// Fallback must stay honest: only use curated real domains and reduce the
	// resulting seed size if the public source is unavailable.
	limit := count
	if limit > len(monitorSeedFallbackDomains) {
		limit = len(monitorSeedFallbackDomains)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, monitorSeedFallbackDomains[i])
	}
	return out
}

func normalizeTarget(raw string) string {
	trimmed := str.Of(raw).Trim().String()
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	host := str.Of(parsed.Hostname()).Trim().ToLower().String()
	if host == "" {
		return ""
	}
	if canonicalTarget, ok := monitorSeedCanonicalTargets[host]; ok {
		return canonicalTarget
	}
	return "https://" + host
}

func buildMonitorName(target string, position int) string {
	if canonicalName, ok := monitorSeedCanonicalNames[target]; ok {
		return canonicalName
	}
	host := str.Of(target).TrimPrefix("https://").TrimPrefix("http://").String()
	if host == "" {
		return fmt.Sprintf("Seed Monitor %d", position)
	}
	parts := strings.Split(host, ".")
	if len(parts) > 0 && str.Of(parts[0]).Trim().String() != "" {
		head := str.Of(parts[0]).Trim().String()
		return formatSeedMonitorHead(head)
	}
	return fmt.Sprintf("Seed Monitor %d", position)
}

func formatSeedMonitorHead(head string) string {
	clean := str.Of(head).Trim().ToLower().String()
	if clean == "" {
		return head
	}
	if acronym, ok := monitorSeedAcronymNames[clean]; ok {
		return acronym
	}
	return str.Of(clean[:1]).ToUpper().String() + clean[1:]
}

func seedValidationProgressStep(total int) int {
	switch {
	case total <= 0:
		return 1
	case total <= 200:
		return 25
	case total <= 2000:
		return 100
	default:
		return 250
	}
}
