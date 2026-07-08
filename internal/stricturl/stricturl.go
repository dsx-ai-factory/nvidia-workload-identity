package stricturl

import (
	"errors"
	"net/url"
)

// ParseHTTP accepts one exact absolute HTTP or HTTPS URL.
func ParseHTTP(raw string) (*url.URL, error) {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.String() != raw {
		return nil, errors.New("must be an exact HTTP(S) URL")
	}
	return u, nil
}

// ParseHTTPOrigin accepts only scheme and authority so callers can append a
// fixed route without inheriting a configured path, query, or fragment.
func ParseHTTPOrigin(raw string) (*url.URL, error) {
	u, err := ParseHTTP(raw)
	if err != nil {
		return nil, err
	}
	if u.User != nil || u.Path != "" || u.RawPath != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must contain only scheme and authority")
	}
	return u, nil
}
