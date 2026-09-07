// Package subscribe implements subscription prefetch and validation (direct first, the
// local mixed-port proxy as fallback stepping stone).
package subscribe

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/httpx"
	"github.com/deadship2003/panoxy/internal/logx"
)

// UA returns the User-Agent used to fetch subscriptions. Measured in practice: the same
// airport returns different node counts for different UAs — ClashMetaForAndroid/clash-verge
// got the most (44 nodes), clash.meta only 41. We pin the UA that yields the maximum.
func UA() string {
	return "ClashMetaForAndroid/2.11.5"
}

// Fetch downloads a subscription to w: direct first, on failure through the local
// mixed-port proxy (when swapping a blocked subscription, the old nodes serve as the
// stepping stone). proxy looks like http://127.0.0.1:33833; empty means direct only.
func Fetch(url, proxy, ua string, w io.Writer) error {
	try := func(p string) error {
		tr := httpx.Transport(p)
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // subscriptions commonly use self-signed certs / bare-IP hosts
		hc := &http.Client{Timeout: 20 * time.Second, Transport: tr}
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := hc.Do(req)
		if err != nil {
			logx.Debug("subscription fetch (%s) failed: %v", orDefault(p, "direct"), err)
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		_, err = io.Copy(w, resp.Body)
		return err
	}
	if err := try(""); err == nil {
		return nil
	} else {
		logx.Step("direct subscription fetch failed, switching to local proxy: %v", err)
	}
	if proxy == "" {
		return fmt.Errorf("direct fetch failed and no local proxy available")
	}
	return try(proxy)
}

// Validate checks subscription content: the format must be recognizable and contain at
// least one node. Covers every standard subscription format (Clash YAML / URI lists /
// sing-box / Surge), see normalize.go; airports often return a web page for an invalid
// token, which must be caught (bash-era lesson from practice).
func Validate(b []byte) error {
	if len(strings.TrimSpace(string(b))) == 0 {
		return fmt.Errorf("subscription content is empty")
	}
	f := Detect(b)
	if f == FormatUnknown {
		return fmt.Errorf("subscription is not in a recognizable format (supported: Clash YAML / base64 or plaintext URI list / sing-box JSON / Surge; airports often return a web page for an invalid token)")
	}
	if nodeCountDetected(b, f) == 0 {
		return fmt.Errorf("no nodes parsed from the subscription (check the link/token; airports may return a web page for invalid requests)")
	}
	return nil
}

// ValidateFile validates a local subscription file and reads its content back.
func ValidateFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read local subscription file: %w", err)
	}
	if err := Validate(b); err != nil {
		return nil, err
	}
	return b, nil
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// CheckName validates a provider name (prevents injection into YAML keys and API paths).
func CheckName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("name may only contain [a-zA-Z0-9_-]: %q", name)
	}
	return nil
}

// CheckURL validates a subscription URL.
func CheckURL(u string) error {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return fmt.Errorf("URL must start with http(s):// (quote the whole argument when passing it on the command line, or run %s sub import and paste it at the prompt)", constants.ProgName)
	}
	return nil
}

func orDefault(s, d string) string {
	if s != "" {
		return s
	}
	return d
}
