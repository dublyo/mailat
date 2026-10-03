package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *ComposeService) uploadJMAPBlob(ctx context.Context, uploadURL, email, password, accountID string, data []byte, contentType string) (string, error) {
	endpoint, err := url.Parse(strings.ReplaceAll(uploadURL, "{accountId}", url.PathEscape(accountID)))
	if err != nil {
		return "", err
	}
	trusted, err := url.Parse(s.cfg.StalwartURL)
	if err != nil {
		return "", err
	}
	if endpoint.Host != trusted.Host || endpoint.Scheme != trusted.Scheme {
		return "", fmt.Errorf("untrusted JMAP upload endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(email, password)
	req.Header.Set("Content-Type", contentType)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("JMAP upload failed with status %d", response.StatusCode)
	}
	var result struct {
		BlobID string `json:"blobId"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result); err != nil {
		return "", err
	}
	if result.BlobID == "" {
		return "", fmt.Errorf("JMAP upload did not return a blob reference")
	}
	return result.BlobID, nil
}
