package scanblock

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Default config values.
const (
	DefaultMinScanRequests = 10
	DefaultMinScanPercent  = 25       // %
	DefaultBlockSeconds    = 600      // 10m
	DefaultRememberSeconds = 6 * 3600 // 6h
	XRealIp                = "X-Real-Ip"
)

type Checker struct {
	allowIPs    []*net.IP
	allowIPsNet []*net.IPNet
}

// Config is the plugin configuration.
type Config struct {
	// MinScanRequests defines the minimum 4xx responses to observe before
	// blocking an IP.
	MinScanRequests uint64

	// MinTotalRequests defines the minimum requests to observe before blocking
	// an IP.
	MinTotalRequests uint64

	// MinScanPercent defines the minimum percent of 4xx responses of total
	// requests before blocking an IP.
	MinScanPercent float64

	// BlockPrivate defines if private IP ranges (RFC1918, RFC4193) should be
	// blocked too.
	BlockPrivate bool

	// PlayGames defines if the the plugin should respond with random 4xx status
	// codes or even kill the connection sometimes.
	PlayGames bool

	// BlockSeconds defines for how many seconds an IP should be blocked.
	BlockSeconds int

	// RememberSeconds defines for how many seconds information about an IP
	// should be cached after it was last seen.
	RememberSeconds int

	IPAllowList []string `json:"ipAllowList,omitempty"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{}
}

// ScanBlock is a scan blocking plugin.
type ScanBlock struct {
	next    http.Handler
	name    string
	config  *Config
	checker *Checker
	cache   *Cache
}

type BlockLog struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Ip        string `json:"ip"`
	FindTime  string `json:"findTime"`
	BlockTime string `json:"blockTime"`
	Total     uint64 `json:"total"`
	Err       uint64 `json:"err"`
}

// New created a new plugin.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	// Apply default values.
	if config.MinScanRequests == 0 {
		config.MinScanRequests = DefaultMinScanRequests
	}
	if config.MinScanPercent == 0 {
		config.MinScanPercent = DefaultMinScanPercent
	}
	if config.BlockSeconds == 0 {
		config.BlockSeconds = DefaultBlockSeconds
	}
	if config.RememberSeconds == 0 {
		config.RememberSeconds = DefaultRememberSeconds
	}

	checker := NewChecker(config.IPAllowList)

	// Log the instantiation of the plugin, including configuration.
	fmt.Fprintf(os.Stdout, "creating scanblock plugin %q with config: %+v\n", name, config)

	// Return new plugin instance.
	return &ScanBlock{
		next:    next,
		name:    name,
		config:  config,
		checker: checker,
		cache:   NewCache(),
	}, nil
}

// ServeHTTP handles a http request.
func (sb *ScanBlock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if request should be blocked and block it.
	entry, ok := sb.check(r)

	// If there was an issue or special bypass condition, bypass this plugin and
	// continue with next handler.
	if !ok {
		sb.next.ServeHTTP(w, r)
		return
	}

	// If we received no cache entry, the request should be blocked.
	if entry == nil {
		sb.block(w, r)
		return
	}

	// Add this request to the counter.
	entry.TotalRequests.Add(1)

	// If we receive an entry, we may continue with the request, but need to wrap
	// the response writer in order to record the status code.
	wrappedResponseWriter := &ResponseWriter{
		ResponseWriter: w,
		cacheEntry:     entry,
	}

	// Continue with next handler.
	sb.next.ServeHTTP(wrappedResponseWriter, r)
}

func (sb *ScanBlock) check(r *http.Request) (entry *CacheEntry, ok bool) {
	// Parse remote address.
	host := r.Header.Get(XRealIp)

	// Parse remote IP address.
	remoteIP := net.ParseIP(host)
	if remoteIP == nil {
		fmt.Fprintf(os.Stderr, "scanblock plugin failed to parse remote IP %q", host)
		return nil, false
	}

	// Ignore loopback IPs.
	if remoteIP.IsLoopback() {
		return nil, false
	}

	// Ignore private IPs if blocking them is not enabled.
	if !sb.config.BlockPrivate && remoteIP.IsPrivate() {
		return nil, false
	}

	if sb.checker != nil && sb.checker.ContainsIP(remoteIP) {
		return nil, false
	}

	// Get entry from cache.
	ipString := remoteIP.String()
	entry = sb.cache.GetEntry(ipString)
	if entry == nil {
		// If not yet in cache, create an entry.
		entry = sb.cache.CreateEntry(ipString)
		entry.FirstSeen.Store(time.Now().Unix())
	}

	// Update last seen when we're done.
	defer entry.LastSeen.Store(time.Now().Unix())

	// Check if we should block.
	switch {
	case entry.Blocking.Load():
		// We are already blocking this IP.

		// Unblock if time since last seen is greater than block duration.
		if entry.LastSeen.Load() < time.Now().Add(-time.Duration(sb.config.BlockSeconds)*time.Second).Unix() {
			entry.Blocking.Store(false)
			return entry, true
		}

		// Otherwise, continue to block.
		return nil, true
	case entry.ScanRequests.Load() < sb.config.MinScanRequests:
		// Not reached minimum scan requests.
		return entry, true
	case entry.TotalRequests.Load() < sb.config.MinTotalRequests:
		// Not reached minimum total requests.
		return entry, true
	case (float64(entry.ScanRequests.Load())/float64(entry.TotalRequests.Load()))*100 < sb.config.MinScanPercent:
		// Not reached minimum scan request percentage.
		return entry, true
	default:
		// All conditions for a block fulfilled, start blocking.

		// Log the block.
		b := BlockLog{
			Type:      "scanblock_plugin",
			Name:      sb.name,
			Ip:        ipString,
			FindTime:  time.Since(time.Unix(entry.FirstSeen.Load(), 0)).Round(time.Second).String(),
			BlockTime: (time.Duration(sb.config.BlockSeconds) * time.Second).String(),
			Total:     entry.TotalRequests.Load(),
			Err:       entry.ScanRequests.Load(),
		}
		jsonData, err := json.Marshal(b)
		if err != nil {
			fmt.Fprint(os.Stderr, "Error marshalling to JSON:", err)
			return
		}
		fmt.Fprint(os.Stdout, string(jsonData))

		// Block this IP.
		entry.Blocking.Store(true)
		return nil, true
	}
}

func NewChecker(allowIPs []string) *Checker {
	if len(allowIPs) == 0 {
		return nil
	}

	checker := &Checker{}

	for _, ipMask := range allowIPs {
		if ipAddr := net.ParseIP(ipMask); ipAddr != nil {
			checker.allowIPs = append(checker.allowIPs, &ipAddr)
		} else {
			_, ipAddr, err := net.ParseCIDR(ipMask)
			if err != nil {
				fmt.Fprintf(os.Stderr, "scanblock plugin failed to parsing CIDR IPs %s: %w", ipAddr, err)
				return nil
			}
			checker.allowIPsNet = append(checker.allowIPsNet, ipAddr)
		}
	}

	return checker
}

func (ip *Checker) ContainsIP(addr net.IP) bool {
	for _, allowIP := range ip.allowIPs {
		if allowIP.Equal(addr) {
			return true
		}
	}

	for _, allowNet := range ip.allowIPsNet {
		if allowNet.Contains(addr) {
			return true
		}
	}

	return false
}
