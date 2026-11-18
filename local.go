package main

import (
	"context"
	_ "embed"
	"errors"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed disposable_domains.txt
var disposableList string

// The same lists the API uses in basic mode, so the local answer is the one
// the API would give without spending a credit or disclosing the address.
var (
	disposable = domainSet(strings.ReplaceAll(disposableList, "\n", ",") + ",mailinator.com,guerrillamail.com,10minutemail.com,temp-mail.org,yopmail.com,throwawaymail.com")
	legacy     = domainSet("wanadoo.es,teleline.es,eresmas.com,inicia.es,jazzfree.com,foros.com,niebla.com,terra.es")
	free       = domainSet("gmail.com,googlemail.com,outlook.com,hotmail.com,hotmail.es,live.com,yahoo.com,yahoo.es,icloud.com,proton.me,protonmail.com")
)

func domainSet(value string) map[string]bool {
	m := map[string]bool{}
	for _, d := range strings.Split(value, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			m[d] = true
		}
	}
	return m
}

// listed reports whether the domain or one of its parents is in the set.
func listed(domain string, set map[string]bool) bool {
	for {
		if set[domain] {
			return true
		}
		i := strings.IndexByte(domain, '.')
		if i < 0 {
			return false
		}
		domain = domain[i+1:]
	}
}

// normalize applies the API's syntax profile (ASCII, no display name):
// trimmed, lower-cased address, its domain and whether the syntax is supported.
func normalize(input string) (email, domain string, ok bool) {
	s := strings.ToLower(strings.TrimSpace(input))
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || len(s) > 254 || strings.ContainsAny(s, "\r\n\x00") {
		return s, "", false
	}
	i := strings.LastIndexByte(s, '@')
	if i < 1 || i > 64 {
		return s, "", false
	}
	d := s[i+1:]
	if a.Address != s {
		local, err := strconv.Unquote(s[:i])
		if err != nil || local+"@"+d != a.Address {
			return s, "", false
		}
	}
	if !validDomain(d) {
		return s, "", false
	}
	for _, c := range s {
		if c < 32 || c > 126 {
			return s, "", false
		}
	}
	return s, d, true
}

func validDomain(d string) bool {
	if len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// nonASCII tells an internationalised address (the API answers `unknown`,
// `address_unsupported`) from plain bad syntax (`invalid`).
func nonASCII(s string) bool {
	for _, c := range s {
		if c > 126 {
			return true
		}
	}
	return false
}

type resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, name string) ([]string, error)
}

func notFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// lookupDomain asks the machine's own resolver whether the domain can receive
// mail: mx, implicit_mx, null_mx, no_mail_host or temporary. Only the domain
// name leaves the machine, and only towards that resolver.
func lookupDomain(r resolver, domain string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	mx, err := r.LookupMX(ctx, domain)
	if len(mx) > 0 {
		if len(mx) == 1 && (mx[0].Host == "." || mx[0].Host == "") {
			return "null_mx" // RFC 7505
		}
		return "mx"
	}
	if err != nil && !notFound(err) {
		return "temporary"
	}
	hosts, err := r.LookupHost(ctx, domain)
	if len(hosts) > 0 {
		return "implicit_mx"
	}
	if err != nil && !notFound(err) {
		return "temporary"
	}
	return "no_mail_host"
}

func lookupDomains(r resolver, domains []string) map[string]string {
	out := make(map[string]string, len(domains))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 20)
	for _, d := range domains {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			status := lookupDomain(r, d)
			mu.Lock()
			out[d] = status
			mu.Unlock()
			<-slots
		}()
	}
	wg.Wait()
	return out
}

// deadDomain maps a DNS status to the contract's reason_code for a domain that
// cannot receive mail, or "". A resolver cannot tell NXDOMAIN from "no
// records" portably, so both are reported as domain_no_mail_host.
func deadDomain(status string) string {
	switch status {
	case "null_mx":
		return "domain_null_mx"
	case "no_mail_host":
		return "domain_no_mail_host"
	}
	return ""
}
