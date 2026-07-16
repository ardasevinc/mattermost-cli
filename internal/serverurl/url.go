package serverurl

import (
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

var (
	ErrInvalid   = errors.New("invalid Mattermost URL")
	ErrAmbiguous = errors.New("Mattermost URL cannot contain credentials, query strings, or fragments")
	ErrScheme    = errors.New("Mattermost URL must use HTTPS, or HTTP on a loopback host")
	ErrPlaintext = errors.New("refusing to send a Mattermost token over plaintext HTTP; use HTTPS or a loopback URL")
)

func Normalize(input string) (string, error) {
	value := strings.TrimSpace(input)
	if strings.Contains(value, "\\") {
		return "", ErrInvalid
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", ErrInvalid
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrAmbiguous
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", ErrScheme
	}
	rawHostname := strings.ToLower(parsed.Hostname())
	hostname := ""
	if address, parseErr := netip.ParseAddr(rawHostname); parseErr == nil {
		hostname = address.String()
	} else {
		if looksLikeAmbiguousIPv4(rawHostname) {
			return "", ErrInvalid
		}
		hostname, err = idna.Lookup.ToASCII(rawHostname)
	}
	if err != nil || hostname == "" || strings.ContainsAny(hostname, "\\\x00\r\n\t") {
		return "", ErrInvalid
	}
	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 0 || portNumber > 65535 {
			return "", ErrInvalid
		}
		port = strconv.Itoa(portNumber)
	}
	if scheme == "http" && !isLoopback(hostname) {
		return "", ErrPlaintext
	}
	if (scheme == "https" && numericPort(port) == 443) || (scheme == "http" && numericPort(port) == 80) {
		port = ""
	}
	return strings.TrimRight(scheme+"://"+canonicalHost(hostname, port)+normalizePath(parsed.EscapedPath()), "/"), nil
}

func looksLikeAmbiguousIPv4(hostname string) bool {
	hostname = strings.TrimSuffix(hostname, ".")
	parts := strings.Split(hostname, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if strings.HasPrefix(strings.ToLower(part), "0x") {
			if len(part) == 2 {
				return false
			}
			for _, char := range part[2:] {
				if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
					return false
				}
			}
			continue
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func numericPort(port string) int {
	value, err := strconv.Atoi(port)
	if err != nil {
		return -1
	}
	return value
}

func normalizePath(escapedPath string) string {
	segments := strings.Split(escapedPath, "/")
	normalized := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch strings.ToLower(segment) {
		case ".", "%2e":
			continue
		case "..", ".%2e", "%2e.", "%2e%2e":
			if len(normalized) > 1 {
				normalized = normalized[:len(normalized)-1]
			}
		default:
			normalized = append(normalized, segment)
		}
	}
	return strings.Join(normalized, "/")
}

func BuildPostPermalink(baseURL, postID string) (string, error) {
	normalized, err := Normalize(baseURL)
	if err != nil {
		return "", err
	}
	return normalized + "/_redirect/pl/" + url.PathEscape(postID), nil
}

func isLoopback(hostname string) bool {
	if hostname == "localhost" {
		return true
	}
	address, err := netip.ParseAddr(hostname)
	return err == nil && address.IsLoopback()
}

func canonicalHost(hostname, port string) string {
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return host
}
