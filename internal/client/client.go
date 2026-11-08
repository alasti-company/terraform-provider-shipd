// Package client implements the small control-plane surface used by Terraform.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultURL = "https://api.useshipd.com"

var ErrNotFound = errors.New("shipd object not found")

type Client struct {
	baseURL, token string
	http           *http.Client
}
type APIError struct {
	Status                   int
	Code, Message, RequestID string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("shipd API %d (%s): %s", e.Status, e.Code, e.Message)
	if e.RequestID != "" {
		message += " [request " + e.RequestID + "]"
	}
	if e.Status == 409 {
		message += ". Refresh the plan before retrying. For slug conflicts, archived slugs may be reserved for seven days."
	}
	return message
}
func IsNotFound(err error) bool {
	var e *APIError
	return errors.Is(err, ErrNotFound) || (errors.As(err, &e) && e.Status == 404)
}

func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("api_url must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	host := u.Hostname()
	if u.Scheme == "http" && host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("api_url must use HTTPS; HTTP is allowed only for localhost or loopback IP addresses")
		}
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("set api_key or SHIPD_API_KEY to a shipd management key or service-account token")
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// RPC retries only reads and idempotency-protected mutations. One key is retained
// for all attempts of a logical mutation; conflicts are never retried.
func (c *Client) RPC(ctx context.Context, operation string, input, output any, mutation bool) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	key := ""
	if mutation {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		key = hex.EncodeToString(b[:])
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/rpc/"+operation, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.token)
		req.Header.Set("x-shipd-client", "api")
		req.Header.Set("User-Agent", "terraform-provider-shipd")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, reqErr := c.http.Do(req)
		delay := time.Duration(attempt+1) * time.Second
		if reqErr == nil {
			data, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode >= 200 && res.StatusCode < 300 {
				if readErr == nil {
					var envelope struct {
						Data json.RawMessage `json:"data"`
					}
					if err := json.Unmarshal(data, &envelope); err == nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
						if output == nil {
							return nil
						}
						return json.Unmarshal(envelope.Data, output)
					}
				}
				// A response may be truncated after a committed write. Reuse its key.
				reqErr = errors.New("invalid or incomplete shipd success response")
			} else {
				var envelope struct {
					Error struct {
						Code      string `json:"code"`
						Message   string `json:"message"`
						RequestID string `json:"requestId"`
					} `json:"error"`
				}
				_ = json.Unmarshal(data, &envelope)
				message := strings.ReplaceAll(envelope.Error.Message, c.token, "[redacted]")
				if message == "" {
					message = http.StatusText(res.StatusCode)
				}
				reqErr = &APIError{Status: res.StatusCode, Code: envelope.Error.Code, Message: message, RequestID: envelope.Error.RequestID}
				if res.StatusCode != 429 && res.StatusCode < 500 {
					return reqErr
				}
				if header := res.Header.Get("Retry-After"); header != "" {
					seconds, err := strconv.Atoi(header)
					if errors.Is(err, strconv.ErrRange) || (err == nil && seconds > 30) {
						return reqErr
					}
					if err == nil {
						delay = time.Duration(seconds) * time.Second
					} else if at, err := http.ParseTime(header); err == nil {
						delay = time.Until(at)
					}
					// Do not retry earlier than the server requested or wait indefinitely.
					if delay > 30*time.Second {
						return reqErr
					}
				}
			}
		}
		if attempt == 2 {
			return fmt.Errorf("shipd request failed after retries: %w", reqErr)
		}
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("shipd request failed")
}

type Project struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organizationId"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	Version        int64  `json:"version"`
}
type Environment struct {
	ID           string `json:"id"`
	ProjectID    string `json:"projectId"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	Type         string `json:"type"`
	IsProduction bool   `json:"isProduction"`
	Version      int64  `json:"version"`
}
type Identity struct {
	Organization struct {
		ID string `json:"id"`
	} `json:"organization"`
}

func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out []Project
	err := c.RPC(ctx, "project/list", struct{}{}, &out, false)
	return out, err
}
func (c *Client) Project(ctx context.Context, id, slug string) (Project, error) {
	items, err := c.Projects(ctx)
	if err != nil {
		return Project{}, err
	}
	for _, p := range items {
		if (id != "" && p.ID == id) || (id == "" && p.Slug == slug) {
			return p, nil
		}
	}
	return Project{}, ErrNotFound
}
func (c *Client) Environments(ctx context.Context, projectID string) ([]Environment, error) {
	var out []Environment
	err := c.RPC(ctx, "environment/list", map[string]any{"projectId": projectID}, &out, false)
	return out, err
}
func (c *Client) Environment(ctx context.Context, projectID, id, slug string) (Environment, error) {
	items, err := c.Environments(ctx, projectID)
	if err != nil {
		return Environment{}, err
	}
	for _, e := range items {
		if (id != "" && e.ID == id) || (id == "" && e.Slug == slug) {
			return e, nil
		}
	}
	return Environment{}, ErrNotFound
}
