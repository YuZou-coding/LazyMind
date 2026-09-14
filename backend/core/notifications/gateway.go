package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type gatewayRejection struct{ Status int }

func (e *gatewayRejection) Error() string { return "notification gateway rejected request" }
func (e *gatewayRejection) Unwrap() error { return ErrInvalid }

func gatewayRequest(ctx context.Context, method, path string, body, result any) error {
	base := strings.TrimRight(os.Getenv("LAZYMIND_CHANNEL_GATEWAY_URL"), "/")
	token := strings.TrimSpace(os.Getenv("LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN"))
	if base == "" || token == "" {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(data))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LazyMind-Internal-Token", token)
	response, err := (&http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 || response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 429 {
		return ErrUnavailable
	}
	if response.StatusCode == 409 {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&failure) == nil && failure.Error.Code == "ACCOUNT_UNAVAILABLE" {
			return &gatewayRejection{Status: response.StatusCode}
		}
		return ErrConflict
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &gatewayRejection{Status: response.StatusCode}
	}
	if result != nil && json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(result) != nil {
		return ErrUnavailable
	}
	return nil
}
