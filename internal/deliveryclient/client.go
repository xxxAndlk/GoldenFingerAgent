// Package deliveryclient 是端侧调用云端临时交付 API 的客户端。
package deliveryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BaseURL  string
	Token    string
	DeviceID string
	HTTP     *http.Client
}

type Client struct {
	baseURL  string
	token    string
	deviceID string
	http     *http.Client
}

func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	token := strings.TrimSpace(cfg.Token)
	if base == "" || token == "" {
		return nil, errors.New("deliveryclient: base_url 和 token 必填")
	}
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Client{baseURL: base, token: token, deviceID: cfg.DeviceID, http: client}, nil
}

type Delivery struct {
	DeliveryID      string     `json:"delivery_id"`
	OwnerUserID     string     `json:"owner_user_id"`
	TargetDeviceID  string     `json:"target_device_id"`
	ArtifactID      string     `json:"artifact_id"`
	ArtifactVersion int        `json:"artifact_version"`
	Name            string     `json:"name"`
	MimeType        string     `json:"mime_type"`
	SizeBytes       int64      `json:"size_bytes"`
	SHA256          string     `json:"sha256"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	AckedAt         *time.Time `json:"acked_at,omitempty"`
}

// HTTPError 携带状态码，供上传队列决定重试策略。
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	body := strings.Join(strings.Fields(e.Body), " ")
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	if body == "" {
		return fmt.Sprintf("deliveryclient: http %d", e.Status)
	}
	return fmt.Sprintf("deliveryclient: http %d: %s", e.Status, body)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

type UploadRequest struct {
	ArtifactID      string
	Version         int
	TargetDeviceID  string
	Name            string
	MimeType        string
	SizeBytes       int64
	SHA256          string
	IdempotencyKey  string
	AllowExecutable bool
	Reader          io.Reader
}

func (c *Client) Upload(ctx context.Context, req UploadRequest) (*Delivery, error) {
	if req.Reader == nil || req.IdempotencyKey == "" {
		return nil, errors.New("deliveryclient: 上传缺少文件或幂等键")
	}
	pr, pw := io.Pipe()
	defer pr.Close()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			fields := map[string]string{
				"artifact_id": req.ArtifactID, "artifact_version": strconv.Itoa(req.Version),
				"target_device_id": req.TargetDeviceID, "name": req.Name,
				"mime_type": req.MimeType, "sha256": req.SHA256,
				"size_bytes": strconv.FormatInt(req.SizeBytes, 10),
			}
			if req.AllowExecutable {
				fields["allow_executable"] = "true"
			}
			for key, value := range fields {
				if err := mw.WriteField(key, value); err != nil {
					return err
				}
			}
			part, err := mw.CreateFormFile("file", req.Name)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, req.Reader); err != nil {
				return err
			}
			return mw.Close()
		}()
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/delivery/v1/artifacts", pr)
	if err != nil {
		pr.Close()
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	var delivery Delivery
	if err := json.Unmarshal(raw, &delivery); err != nil {
		return nil, err
	}
	return &delivery, nil
}

func (c *Client) Inbox(ctx context.Context, limit int) ([]Delivery, error) {
	path := "/api/delivery/v1/inbox"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	var decoded struct {
		Deliveries []Delivery `json:"deliveries"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded.Deliveries, nil
}

func (c *Client) Get(ctx context.Context, id string) (*Delivery, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/delivery/v1/artifacts/"+id, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	var delivery Delivery
	if err := json.Unmarshal(raw, &delivery); err != nil {
		return nil, err
	}
	return &delivery, nil
}

// Download 返回可流式读取的响应体；调用方负责关闭。
type Download struct {
	Body   io.ReadCloser
	Name   string
	Mime   string
	SHA256 string
	Size   int64
}

func (c *Client) Download(ctx context.Context, id string) (*Download, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/delivery/v1/artifacts/"+id+"/content", nil, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	name := "download"
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = params["filename"]
	}
	return &Download{
		Body: resp.Body, Name: name, Mime: resp.Header.Get("Content-Type"),
		SHA256: resp.Header.Get("X-Content-Hash"), Size: resp.ContentLength,
	}, nil
}

func (c *Client) Ack(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/delivery/v1/artifacts/"+id+"/ack", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	return nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/api/delivery/v1/artifacts/"+id, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	return nil
}
