package runnerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

func New(socketPath, token string) *Client {
	return &Client{
		httpClient: &http.Client{
			Transport: unixTransport(socketPath),
			Timeout:   30 * time.Second,
		},
		baseURL: "http://runner",
		token:   token,
	}
}

func (c *Client) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/healthz",
		nil,
	)
	if err != nil {
		return err
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &StatusError{StatusCode: response.StatusCode}
	}
	return nil
}

type StatusError struct {
	StatusCode int
}

func (e *StatusError) Error() string {
	data, _ := json.Marshal(map[string]int{"status_code": e.StatusCode})
	return "runner request failed: " + string(data)
}
