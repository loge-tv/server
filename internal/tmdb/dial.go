package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Dialer connects like net.Dialer, except when the system resolver answers
// only loopback addresses for a public name: TMDB's DNS (Route 53 by
// location) gives 127.0.0.1 to Russian client subnets, which Google and
// Yandex pass on (EDNS client subnet) and routers using them repeat. Then
// the name is asked over DNS-over-HTTPS from Cloudflare, which passes no
// client subnet, and the real addresses work.
type Dialer struct {
	Net *net.Dialer
	// Lookup and DoH are replaceable in tests.
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
	DoH    func(ctx context.Context, host string) ([]net.IP, error)
}

func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := d.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		c, err := d.Net.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// Resolve gives the addresses to dial for host.
func (d *Dialer) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil || host == "localhost" {
		if ip == nil {
			ip = net.IPv4(127, 0, 0, 1)
		}
		return []net.IP{ip}, nil
	}
	lookup := d.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	ips, err := lookup(ctx, host)
	if err == nil && !allLoopback(ips) {
		return ips, nil
	}
	doh := d.DoH
	if doh == nil {
		doh = cloudflareDoH
	}
	alt, derr := doh(ctx, host)
	if derr == nil && len(alt) > 0 && !allLoopback(alt) {
		return alt, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%s resolves to %v only (DNS over HTTPS: %v)", host, ips, derr)
}

func allLoopback(ips []net.IP) bool {
	for _, ip := range ips {
		if !ip.IsLoopback() && !ip.IsUnspecified() {
			return false
		}
	}
	return true
}

var dohClient = &http.Client{Timeout: 10 * time.Second}

// cloudflareDoH asks A records over DNS-over-HTTPS (JSON API).
func cloudflareDoH(ctx context.Context, host string) ([]net.IP, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://cloudflare-dns.com/dns-query?type=A&name="+url.QueryEscape(host), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := dohClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doh: status %d", resp.StatusCode)
	}
	var body struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, a := range body.Answer {
		if a.Type == 1 {
			if ip := net.ParseIP(a.Data); ip != nil {
				ips = append(ips, ip)
			}
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("doh: no A records")
	}
	return ips, nil
}
