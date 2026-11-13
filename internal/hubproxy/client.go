package hubproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

const registryHost = "registry-1.docker.io"
const authHost = "auth.docker.io"
const accept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

type Error struct {
	Status                    int
	Code, Message, RetryAfter string
}

func (e *Error) Error() string { return e.Message }
func unavailable(message string) error {
	return &Error{Status: 502, Code: "UNAVAILABLE", Message: message}
}
func allowedHost(host string) bool {
	return host == registryHost || host == authHost || host == "production.cloudflare.docker.com" || host == "production.cloudfront.docker.com" || host == "docker-images-prod.6aa30f8b08e16409b46e0173d6de2f56.r2.cloudflarestorage.com"
}
func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	// GlobalUnicast alone includes private and documentation ranges.
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "2002::/16"} {
		if netip.MustParsePrefix(p).Contains(ip) {
			return false
		}
	}
	return true
}
func safeClient() *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 16, MaxConnsPerHost: 16, DisableCompression: true}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || port != "443" || !allowedHost(host) {
			return nil, errors.New("上游地址不受信任")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("上游地址无法解析")
		}
		for _, ip := range ips {
			if !publicIP(ip) {
				return nil, errors.New("上游解析到非公共地址")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return idleConn{Conn: conn}, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type idleConn struct{ net.Conn }

func (c idleConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	return c.Conn.Read(p)
}
func checkURL(u *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && u.Port() == "" && allowedHost(u.Hostname()) && u.Fragment == ""
}
func (c *Cache) token(ctx context.Context, name string) (string, error) {
	// Fetch a narrowly scoped anonymous Hub token. No light credential is forwarded.
	u := "https://" + authHost + "/token?service=registry.docker.io&scope=" + url.QueryEscape("repository:"+name+":pull")
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", unavailable("Docker Hub 认证连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", upstreamError(resp)
	}
	var doc struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &doc) != nil {
		return "", unavailable("Docker Hub 认证响应无效")
	}
	if doc.Token == "" {
		doc.Token = doc.AccessToken
	}
	if doc.Token == "" || len(doc.Token) > 16384 {
		return "", unavailable("Docker Hub 未返回有效令牌")
	}
	return doc.Token, nil
}
func (c *Cache) request(ctx context.Context, name, kind, ref, method string) (*http.Response, error) {
	token, err := c.token(ctx, name)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse("https://" + registryHost + "/v2/" + name + "/" + kind + "/" + ref)
	for hop := 0; hop < 6; hop++ {
		if !checkURL(u) || (kind != "blobs" && u.Host != registryHost) {
			return nil, unavailable("拒绝不受信任的上游跳转")
		}
		req, _ := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if u.Host == registryHost {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", accept)
		resp, e := c.client.Do(req)
		if e != nil {
			return nil, unavailable("Docker Hub 请求失败或超时")
		}
		if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
			next, e := resp.Location()
			resp.Body.Close()
			if e != nil {
				return nil, unavailable("Docker Hub 跳转无效")
			}
			u = next
			continue
		}
		if resp.StatusCode != 200 {
			e = upstreamError(resp)
			if known, ok := e.(*Error); ok && kind == "blobs" && known.Status == 404 {
				known.Code = "BLOB_UNKNOWN"
			}
			resp.Body.Close()
			return nil, e
		}
		return resp, nil
	}
	return nil, unavailable("Docker Hub 跳转次数过多")
}
func upstreamError(resp *http.Response) error {
	status, code, message := 502, "UNAVAILABLE", fmt.Sprintf("Docker Hub 返回错误（%d）", resp.StatusCode)
	switch resp.StatusCode {
	case 404:
		status, code, message = 404, "MANIFEST_UNKNOWN", "Docker Hub 中不存在此镜像或内容"
	case 401, 403:
		status, code, message = 403, "DENIED", "Docker Hub 拒绝拉取此镜像"
	case 429:
		status, code, message = 429, "TOOMANYREQUESTS", "Docker Hub 拉取限流，请稍后重试"
	}
	retry := resp.Header.Get("Retry-After")
	if len(retry) > 128 {
		retry = ""
	}
	return &Error{status, code, message, retry}
}
