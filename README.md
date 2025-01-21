# scanblock
scanblock Traefik plugin that blocks scanner IPs(X-Real-Ip) by counting 4xx status codes until a limit is hit.

Can also play games with scanners.

Inspirations taken from:
https://github.com/safing/scanblock

### Config

```
// MinScanRequests defines the minimum 4xx responses to observe before blocking an IP.
minScanRequests: "2"

// MinTotalRequests defines the minimum requests to observe before blocking an IP. 25 representative 25%
minTotalRequests: "4"

// MinScanPercent defines the minimum percent of 4xx responses of total requests before blocking an IP.
minScanPercent: "50"

// BlockPrivate defines if private IP ranges (RFC1918, RFC4193) should be blocked too.
blockPrivate: "true"

// PlayGames defines if the the plugin should respond with random 4xx status codes or even kill the connection sometimes.
playGames: "false"

// BlockSeconds defines for how many seconds an IP should be blocked.
blockSeconds: "5"

// RememberSeconds defines for how many seconds information about an IP should be cached after it was last seen.
rememberSeconds: "10"

// IP White list
ipAllowList:
    - 24.0.0.0/12
    - 24.16.0.0/13

```