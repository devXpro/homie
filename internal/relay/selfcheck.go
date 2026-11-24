package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// InstanceHeader carries the hub's instance id on every hub response (PLAN §5.4).
const InstanceHeader = "X-Homie-Instance"

// selfCheckTimeout bounds one self-check request.
const selfCheckTimeout = 10 * time.Second

// SelfCheck proves that https://<domain>/healthz reaches this hub: the answer must be 200 and carry our instance id
// in X-Homie-Instance. It trusts the system roots.
func SelfCheck(ctx context.Context, domain, instanceID string) error {
	return Config{Domain: domain}.SelfCheck(ctx, instanceID)
}

// SelfCheck is the package-level SelfCheck with this config's trust (CAFile) and dial address (ServerAddr).
func (c Config) SelfCheck(ctx context.Context, instanceID string) error {
	if err := validateDomain(c.Domain); err != nil {
		return err
	}
	hc, err := c.httpClient()
	if err != nil {
		return err
	}
	defer hc.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, selfCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+c.Domain+"/healthz", http.NoBody)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("self-check: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	got := resp.Header.Get(InstanceHeader)
	switch {
	case resp.StatusCode == http.StatusNotFound && got == "":
		return errors.New("self-check: the relay answers but the home is offline (no frp client connected)")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("self-check: status %d", resp.StatusCode)
	case got == "":
		return errors.New("self-check: the answer has no instance id; the domain does not reach a Homie hub")
	case got != instanceID:
		return errors.New("self-check: the domain reaches another Homie hub")
	}
	return nil
}

// httpClient returns a client that trusts what the frp client trusts and dials what it dials.
func (c Config) httpClient() (*http.Client, error) {
	tlsCfg := &tls.Config{ServerName: c.Domain, MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read relay CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("relay CA file %s holds no PEM certificates", c.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	dialer := &net.Dialer{Timeout: selfCheckTimeout}
	tr := &http.Transport{
		TLSClientConfig:   tlsCfg,
		ForceAttemptHTTP2: true,
		Proxy:             http.ProxyFromEnvironment,
	}
	if c.ServerAddr != "" {
		addr := c.ServerAddr
		tr.Proxy = nil
		tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		}
	} else {
		tr.DialContext = dialer.DialContext
	}
	return &http.Client{
		Transport: tr,
		// A redirect would mean the domain is not served by the hub directly.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
