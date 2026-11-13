package hubproxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRejectUntrustedRedirectsAndTokenRealm(t *testing.T) {
	for _, location := range []string{"http://production.cloudflare.docker.com/blob", "https://127.0.0.1/blob", "https://evil.test/blob", "https://auth.docker.io:444/blob", "https://user:pass@production.cloudflare.docker.com/blob"} {
		t.Run(location, func(t *testing.T) {
			f := fixture(t)
			count := 0
			f.c.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				count++
				h := http.Header{}
				body := `{"token":"upstream-only"}`
				status := 200
				if r.URL.Host == registryHost {
					status = 307
					h.Set("Location", location)
				} else if r.URL.Host != authHost {
					t.Errorf("untrusted outbound %s", r.URL)
				}
				return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			if _, err := f.c.request(context.Background(), "library/alpine", "blobs", f.dgst.String(), "GET"); err == nil {
				t.Fatal("接受了恶意跳转")
			}
			if count != 2 {
				t.Fatalf("向不受信地址发送了请求 %d", count)
			}
		})
	}
	f := fixture(t)
	f.c.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: http.Header{"Www-Authenticate": []string{`Bearer realm="http://169.254.169.254/token"`}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, err := f.c.token(context.Background(), "library/alpine"); err == nil {
		t.Fatal("接受了非固定认证端点")
	}
}
func TestCDNRedirectDropsAuthorization(t *testing.T) {
	f := fixture(t)
	cdn := false
	f.c.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		status := 200
		body := []byte(`{"token":"private-hub-token"}`)
		switch r.URL.Host {
		case registryHost:
			status = 307
			h.Set("Location", "https://production.cloudflare.docker.com/anonymous-signed-url")
		case "production.cloudflare.docker.com":
			cdn = true
			if r.Header.Get("Authorization") != "" {
				t.Fatal("跨主机泄漏令牌")
			}
			body = f.data
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	resp, err := f.c.request(context.Background(), "library/alpine", "blobs", f.dgst.String(), "GET")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !cdn {
		t.Fatal("未到达 CDN")
	}
	for _, s := range []string{"https://registry-1.docker.io/v2/", "https://docker-images-prod.6aa30f8b08e16409b46e0173d6de2f56.r2.cloudflarestorage.com/data"} {
		u, _ := url.Parse(s)
		if !checkURL(u) {
			t.Error(s)
		}
	}
}
