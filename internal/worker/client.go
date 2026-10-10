package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const (
	requestTimeout   = 10 * time.Second
	maxErrorBodySize = 64 << 10
)

// ErrUnknownWorker is returned by Heartbeat when the server does not
// recognise the worker ID, for example after its record was deleted.
var ErrUnknownWorker = errors.New("worker is not registered")

// APIError is a non-success response from the API server.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("api returned %d %s", e.Status, http.StatusText(e.Status))
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Client calls the worker endpoints of the API server.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// longPoll has no overall timeout; each claim sets its own deadline.
	longPoll *http.Client
}

// NewClient returns a client for the API at baseURL that authenticates with token.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		http:     &http.Client{Timeout: requestTimeout},
		longPoll: &http.Client{},
	}
}

// Claim starts the worker's next assigned job, waiting up to wait for one to
// be assigned. found is false when none arrived in time.
func (c *Client) Claim(ctx context.Context, id uuid.UUID, wait time.Duration) (claim workerapi.ClaimResponse, found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, wait+requestTimeout)
	defer cancel()

	path := workerapi.ClaimPath(id) + "?wait=" + url.QueryEscape(wait.String())
	resp, err := c.send(ctx, c.longPoll, path, struct{}{})
	if err != nil {
		return claim, false, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return claim, false, nil
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(&claim); err != nil {
			return claim, false, fmt.Errorf("decode claim: %w", err)
		}
		return claim, true, nil
	case http.StatusNotFound:
		return claim, false, ErrUnknownWorker
	default:
		return claim, false, apiError(resp)
	}
}

// Register announces the worker and returns its ID.
func (c *Client) Register(ctx context.Context, req workerapi.RegisterRequest) (workerapi.RegisterResponse, error) {
	var resp workerapi.RegisterResponse
	err := c.post(ctx, workerapi.RegisterPath, req, http.StatusOK, &resp)
	return resp, err
}

// Heartbeat reports that the worker is alive along with current host usage.
func (c *Client) Heartbeat(ctx context.Context, id uuid.UUID, req workerapi.HeartbeatRequest) error {
	err := c.post(ctx, workerapi.HeartbeatPath(id), req, http.StatusNoContent, nil)
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status == http.StatusNotFound {
		return ErrUnknownWorker
	}
	return err
}

func (c *Client) post(ctx context.Context, path string, body any, want int, out any) error {
	resp, err := c.send(ctx, c.http, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != want {
		return apiError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// send posts body as JSON with the worker token. The caller closes the body.
func (c *Client) send(ctx context.Context, client *http.Client, path string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post %s: %w", path, err)
	}
	return resp, nil
}

func apiError(resp *http.Response) error {
	var p struct {
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxErrorBodySize)).Decode(&p)
	return &APIError{Status: resp.StatusCode, Detail: p.Detail}
}
