package arena

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func LocalEndpoint(r Runtime) (string, error) {
	endpoint := str(r.Options["endpoint"])
	if endpoint == "" {
		switch r.Options["backend"] {
		case "ollama":
			endpoint = "http://127.0.0.1:11434"
		case "lm-studio":
			endpoint = "http://127.0.0.1:1234/v1"
		default:
			return "", fmt.Errorf("select the local backend endpoint explicitly")
		}
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("local endpoint must be a loopback HTTP URL without credentials, query, or fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("local backend must run on this machine (loopback address required)")
	}
	return strings.TrimRight(endpoint, "/"), nil
}
