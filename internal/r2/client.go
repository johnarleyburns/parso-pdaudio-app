package r2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	region       = "auto"
	service      = "s3"
	unsignedBody = "UNSIGNED-PAYLOAD"
)

// Client is an S3-compatible R2 client.
type Client struct {
	cfg  *Config
	http *http.Client
}

// New builds a Client from config.
func New(cfg *Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 10 * time.Minute}}
}

// Config returns the underlying config (read-only use).
func (c *Client) Config() *Config { return c.cfg }

func (c *Client) objectURL(key string) string {
	return c.cfg.Endpoint() + "/" + c.cfg.Bucket + "/" + escapeKey(key)
}

// escapeKey percent-encodes a key path while preserving slashes.
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// HeadObject returns whether an object exists and its size.
func (c *Client) HeadObject(ctx context.Context, key string) (exists bool, size int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.objectURL(key), nil)
	if err != nil {
		return false, 0, err
	}
	if err := c.sign(req, emptyHash); err != nil {
		return false, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, 0, fmt.Errorf("HEAD %s: http %d", key, resp.StatusCode)
	}
	return true, resp.ContentLength, nil
}

// PutFile uploads a local file to key with the given content type, computing the
// payload hash for a fully-signed request.
func (c *Client) PutFile(ctx context.Context, key, contentType, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	payloadHash := hex.EncodeToString(h.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.objectURL(key), f)
	if err != nil {
		return err
	}
	req.ContentLength = fi.Size()
	req.Header.Set("Content-Type", contentType)
	if err := c.sign(req, payloadHash); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("PUT %s: http %d: %s", key, resp.StatusCode, string(body))
	}
	return nil
}

// GetToFile downloads an object to a local path.
func (c *Client) GetToFile(ctx context.Context, key, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.objectURL(key), nil)
	if err != nil {
		return err
	}
	if err := c.sign(req, emptyHash); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("GET %s: http %d: %s", key, resp.StatusCode, string(body))
	}
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// PublicURL returns an unauthenticated URL for a key if a public base is set.
func (c *Client) PublicURL(key string) (string, bool) {
	if c.cfg.PublicBaseURL == "" {
		return "", false
	}
	return strings.TrimRight(c.cfg.PublicBaseURL, "/") + "/" + escapeKey(key), true
}

// PresignGet returns a presigned GET URL valid for the given duration.
func (c *Client) PresignGet(key string, expires time.Duration) (string, error) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	credScope := dateStamp + "/" + region + "/" + service + "/aws4_request"

	host := c.cfg.Host()
	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", c.cfg.AccessKeyID+"/"+credScope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(expires.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")

	canonicalURI := "/" + c.cfg.Bucket + "/" + escapeKey(key)
	canonicalQuery := q.Encode()
	canonicalHeaders := "host:" + host + "\n"
	canonicalRequest := strings.Join([]string{
		http.MethodGet, canonicalURI, canonicalQuery,
		canonicalHeaders, "host", unsignedBody,
	}, "\n")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, credScope, hashHex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(c.cfg.SecretAccessKey, dateStamp), stringToSign))
	q.Set("X-Amz-Signature", signature)

	return "https://" + host + canonicalURI + "?" + q.Encode(), nil
}

const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")

// sign adds AWS SigV4 headers to req. payloadHash is the hex sha256 of the body
// (or emptyHash / UNSIGNED-PAYLOAD).
func (c *Client) sign(req *http.Request, payloadHash string) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("Host", req.URL.Host)

	signedHeaders, canonicalHeaders := canonicalizeHeaders(req)
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := req.URL.Query().Encode()
	canonicalRequest := strings.Join([]string{
		req.Method, canonicalURI, canonicalQuery,
		canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	credScope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, credScope, hashHex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(c.cfg.SecretAccessKey, dateStamp), stringToSign))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKeyID, credScope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
	return nil
}

func canonicalizeHeaders(req *http.Request) (signedHeaders, canonicalHeaders string) {
	names := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if req.Header.Get("Content-Type") != "" {
		names = append(names, "content-type")
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		var v string
		switch n {
		case "host":
			v = req.URL.Host
		default:
			v = req.Header.Get(n)
		}
		b.WriteString(n + ":" + strings.TrimSpace(v) + "\n")
	}
	return strings.Join(names, ";"), b.String()
}

func signingKey(secret, dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
