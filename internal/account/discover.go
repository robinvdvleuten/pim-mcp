package account

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/transport"
)

// Where discovery found a setting.
const (
	SourcePreset     = "built-in preset"
	SourceDNS        = "DNS SRV record"
	SourceWellKnown  = "well-known URL"
	SourceAutoconfig = "provider's autoconfig file"
	SourceISPDB      = "Mozilla ISPDB"
	SourceGuess      = "a guess from the domain, please check it"
)

const (
	discoveryTimeout   = 5 * time.Second
	maxDiscoveryHops   = 5
	maxAutoconfigBytes = 1 << 20
	httpsPort          = 443
)

// ErrNotDiscovered means no source knew the service.
var ErrNotDiscovered = errors.New("could not be discovered")

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Resolver is the part of net.Resolver that discovery uses.
type Resolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Discoverer finds a provider's servers from an email address. It only looks
// things up: the password isn't known yet, and the address itself is never sent.
// Whatever it finds comes from DNS and websites, so the user confirms it before
// any password goes there.
type Discoverer struct {
	Resolver  Resolver          // defaults to net.DefaultResolver
	Transport http.RoundTripper // defaults to http.DefaultTransport
}

// FoundIMAP is a discovered mail server. Username is empty if the source had
// no opinion on it.
type FoundIMAP struct {
	IMAP     IMAP
	Username string
	Source   string
}

// FoundCalDAV is a discovered calendar server.
type FoundCalDAV struct {
	CalDAV CalDAV
	Source string
}

// DiscoverCalDAV tries a preset, then DNS (RFC 6764), then the domain's
// well-known URL. The calendars themselves are looked up later, with a login.
func (d *Discoverer) DiscoverCalDAV(ctx context.Context, email string) (*FoundCalDAV, error) {
	domain, err := emailDomain(email)
	if err != nil {
		return nil, err
	}
	if p, ok := presets[domain]; ok {
		return &FoundCalDAV{CalDAV: p.caldav, Source: SourcePreset}, nil
	}

	if base, err := d.caldavFromDNS(ctx, domain); err == nil {
		return &FoundCalDAV{CalDAV: CalDAV{URL: base}, Source: SourceDNS}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if base, err := d.davEndpoint(ctx, "https://"+domain+wellKnownCalDAV); err == nil {
		return &FoundCalDAV{CalDAV: CalDAV{URL: base}, Source: SourceWellKnown}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("CalDAV server for %s %w", domain, ErrNotDiscovered)
}

const wellKnownCalDAV = "/.well-known/caldav"

func (d *Discoverer) caldavFromDNS(ctx context.Context, domain string) (string, error) {
	host, err := d.lookupSRV(ctx, "caldavs", domain)
	if err != nil {
		return "", err
	}
	path := wellKnownCalDAV
	// A missing TXT record is normal: the path is then the well-known one.
	records, _ := d.resolver().LookupTXT(ctx, "_caldavs._tcp."+domain)
	for _, record := range records {
		if value, ok := strings.CutPrefix(record, "path="); ok && strings.HasPrefix(value, "/") {
			path = value
		}
	}
	return d.davEndpoint(ctx, "https://"+host+path)
}

// lookupSRV returns the best host and port for a service. net.Resolver sorts
// the records by priority.
func (d *Discoverer) lookupSRV(ctx context.Context, service, domain string) (string, error) {
	_, records, err := d.resolver().LookupSRV(ctx, service, "tcp", domain)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		target := strings.ToLower(strings.TrimSuffix(record.Target, "."))
		// A target of "." says the service isn't offered (RFC 2782).
		if !hostnamePattern.MatchString(target) {
			continue
		}
		if record.Port == httpsPort {
			return target, nil
		}
		return net.JoinHostPort(target, strconv.Itoa(int(record.Port))), nil
	}
	return "", errors.New("no usable SRV record")
}

// davEndpoint finds where a DAV server answers. A well-known URL gets one GET,
// because some servers only redirect that. Following more would leave the DAV
// path: Radicale's root redirects GET to its web interface. From there on,
// PROPFIND follows redirects and tells a DAV server from a website that answers
// anything with a page.
func (d *Discoverer) davEndpoint(ctx context.Context, start string) (string, error) {
	current := start
	if strings.HasSuffix(start, wellKnownCalDAV) {
		_, next, err := d.probe(ctx, http.MethodGet, current)
		if err != nil {
			return "", err
		}
		if next != "" {
			current = next
		}
	}
	for range maxDiscoveryHops {
		status, next, err := d.probe(ctx, "PROPFIND", current)
		if err != nil {
			return "", err
		}
		if next != "" {
			current = next
			continue
		}
		if status != http.StatusMultiStatus && status != http.StatusUnauthorized {
			return "", fmt.Errorf("not a DAV server: status %d", status)
		}
		return current, nil
	}
	return "", errors.New("too many redirects")
}

// probe sends one request and reports where a redirect points, if it does.
func (d *Discoverer) probe(ctx context.Context, method, target string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Depth", "0")
	client := &http.Client{
		Transport: d.Transport,
		Timeout:   discoveryTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return resp.StatusCode, "", nil
	}
	location, err := req.URL.Parse(resp.Header.Get("Location"))
	if err != nil {
		return 0, "", err
	}
	if err := transport.CheckURL(location); err != nil {
		return 0, "", err
	}
	return resp.StatusCode, location.String(), nil
}

// DiscoverIMAP tries a preset, the provider's autoconfig file, Mozilla's ISPDB
// if allowed (which tells Mozilla the domain), DNS (RFC 6186), and finally
// guesses imap.<domain>.
func (d *Discoverer) DiscoverIMAP(ctx context.Context, email string, useISPDB bool) (*FoundIMAP, error) {
	domain, err := emailDomain(email)
	if err != nil {
		return nil, err
	}
	if p, ok := presets[domain]; ok {
		return &FoundIMAP{IMAP: p.imap, Source: SourcePreset}, nil
	}

	sources := []struct {
		url    string
		source string
	}{
		{"https://autoconfig." + domain + "/mail/config-v1.1.xml", SourceAutoconfig},
		{"https://" + domain + "/.well-known/autoconfig/mail/config-v1.1.xml", SourceAutoconfig},
	}
	if useISPDB {
		sources = append(sources, struct {
			url    string
			source string
		}{"https://autoconfig.thunderbird.net/v1.1/" + url.PathEscape(domain), SourceISPDB})
	}
	for _, source := range sources {
		if found := d.fetchAutoconfig(ctx, source.url, email); found != nil {
			found.Source = source.source
			return found, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	if host, err := d.lookupSRV(ctx, "imaps", domain); err == nil {
		return &FoundIMAP{IMAP: IMAP{Addr: host, Security: SecurityTLS}, Source: SourceDNS}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &FoundIMAP{
		IMAP:   IMAP{Addr: net.JoinHostPort("imap."+domain, implicitTLSPort), Security: SecurityTLS},
		Source: SourceGuess,
	}, nil
}

// fetchAutoconfig returns nil on any failure: a missing or broken file just
// means trying the next source.
func (d *Discoverer) fetchAutoconfig(ctx context.Context, target, email string) *FoundIMAP {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	client := &http.Client{
		Transport: d.Transport,
		Timeout:   discoveryTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxDiscoveryHops {
				return errors.New("too many redirects")
			}
			return transport.CheckURL(req.URL)
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAutoconfigBytes))
	if err != nil {
		return nil
	}
	return parseAutoconfig(data, email)
}

// parseAutoconfig reads Thunderbird's autoconfig format, taking the first IMAP
// server that encrypts its connection.
func parseAutoconfig(data []byte, email string) *FoundIMAP {
	var config struct {
		Servers []struct {
			Type     string `xml:"type,attr"`
			Hostname string `xml:"hostname"`
			Port     int    `xml:"port"`
			Socket   string `xml:"socketType"`
			Username string `xml:"username"`
		} `xml:"emailProvider>incomingServer"`
	}
	if err := xml.Unmarshal(data, &config); err != nil {
		return nil
	}

	local, domain, _ := strings.Cut(email, "@")
	expand := strings.NewReplacer("%EMAILADDRESS%", email, "%EMAILLOCALPART%", local, "%EMAILDOMAIN%", domain)
	for _, server := range config.Servers {
		var security string
		switch strings.ToUpper(server.Socket) {
		case "SSL":
			security = SecurityTLS
		case "STARTTLS":
			security = SecurityStartTLS
		default:
			continue
		}
		host := strings.ToLower(expand.Replace(server.Hostname))
		if server.Type != "imap" || !hostnamePattern.MatchString(host) || server.Port < 1 || server.Port > 65535 {
			continue
		}
		return &FoundIMAP{
			IMAP:     IMAP{Addr: net.JoinHostPort(host, strconv.Itoa(server.Port)), Security: security},
			Username: expand.Replace(server.Username),
		}
	}
	return nil
}

func (d *Discoverer) resolver() Resolver {
	if d.Resolver != nil {
		return d.Resolver
	}
	return net.DefaultResolver
}

func emailDomain(email string) (string, error) {
	_, domain, ok := strings.Cut(email, "@")
	domain = strings.ToLower(domain)
	if !ok || !hostnamePattern.MatchString(domain) {
		return "", fmt.Errorf("servers for %q %w", email, ErrNotDiscovered)
	}
	return domain, nil
}
