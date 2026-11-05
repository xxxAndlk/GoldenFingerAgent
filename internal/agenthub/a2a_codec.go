package agenthub

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// a2aCodec 按 Agent Card 声明的协议版本构造请求并解析结果。
type a2aCodec interface {
	Version() string
	Method() string
	Message(text string) map[string]any
	OutputModes() []string
	Parse(body []byte) (a2aParsed, error)
}

func codecFor(version string) a2aCodec {
	switch normalizeA2AVersion(version) {
	case "1.0":
		return a2aCodecV1{}
	default:
		return a2aCodecV03{}
	}
}

func normalizeA2AVersion(version string) string {
	value := strings.ToLower(strings.TrimSpace(version))
	value = strings.TrimPrefix(value, "v")
	switch {
	case value == "1" || strings.HasPrefix(value, "1."):
		return "1.0"
	case value == "0.3" || strings.HasPrefix(value, "0.3"):
		return "0.3"
	default:
		return ""
	}
}

type a2aWirePart struct {
	Kind      string          `json:"kind,omitempty"`
	Type      string          `json:"type,omitempty"`
	Text      string          `json:"text,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	File      *a2aWireFile    `json:"file,omitempty"`
	URL       string          `json:"url,omitempty"`
	Raw       string          `json:"raw,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	SHA256    string          `json:"sha256,omitempty"`
}

type a2aWireFile struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Bytes    string `json:"bytes,omitempty"`
	URI      string `json:"uri,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type a2aWireMessage struct {
	Kind      string        `json:"kind,omitempty"`
	Role      string        `json:"role"`
	Parts     []a2aWirePart `json:"parts"`
	MessageID string        `json:"messageId,omitempty"`
	TaskID    string        `json:"taskId,omitempty"`
	ContextID string        `json:"contextId,omitempty"`
}

type a2aWireArtifact struct {
	ArtifactID string        `json:"artifactId,omitempty"`
	Name       string        `json:"name,omitempty"`
	Parts      []a2aWirePart `json:"parts"`
}

type a2aWireResult struct {
	Kind   string        `json:"kind,omitempty"`
	ID     string        `json:"id,omitempty"`
	Parts  []a2aWirePart `json:"parts,omitempty"`
	Status struct {
		State   string         `json:"state"`
		Message a2aWireMessage `json:"message"`
	} `json:"status"`
	Artifacts []a2aWireArtifact `json:"artifacts,omitempty"`
	History   []a2aWireMessage  `json:"history,omitempty"`
}

type rawArtifact struct {
	AgentArtifact
	Raw []byte
}

type a2aParsed struct {
	Text      string
	State     string
	TaskID    string
	Artifacts []rawArtifact
}

const maxInlineArtifact = 8 << 20

func parseA2AResult(body []byte) (a2aParsed, error) {
	var rpc struct {
		Result a2aWireResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return a2aParsed{}, fmt.Errorf("A2A 响应解析失败：%w", err)
	}
	if rpc.Error != nil {
		return a2aParsed{}, fmt.Errorf("A2A %d：%s", rpc.Error.Code, rpc.Error.Message)
	}
	result := rpc.Result
	parsed := a2aParsed{
		State:  normalizeTaskState(result.Status.State),
		TaskID: result.ID,
	}
	var text strings.Builder
	if err := collectParts(result.Parts, &text, &parsed.Artifacts); err != nil {
		return a2aParsed{}, err
	}
	if err := collectParts(result.Status.Message.Parts, &text, &parsed.Artifacts); err != nil {
		return a2aParsed{}, err
	}
	for _, artifact := range result.Artifacts {
		before := len(parsed.Artifacts)
		artifactText := strings.Builder{}
		if err := collectParts(artifact.Parts, &artifactText, &parsed.Artifacts); err != nil {
			return a2aParsed{}, err
		}
		text.WriteString(artifactText.String())
		for i := before; i < len(parsed.Artifacts); i++ {
			if parsed.Artifacts[i].ID == "" {
				parsed.Artifacts[i].ID = artifact.ArtifactID
			}
			if (parsed.Artifacts[i].Name == "" || parsed.Artifacts[i].Name == "artifact") && artifact.Name != "" {
				parsed.Artifacts[i].Name = artifact.Name
			}
		}
	}
	if text.Len() == 0 && len(result.History) > 0 {
		if err := collectParts(result.History[len(result.History)-1].Parts, &text, &parsed.Artifacts); err != nil {
			return a2aParsed{}, err
		}
	}
	parsed.Text = strings.TrimSpace(text.String())
	parsed.Artifacts = dedupeArtifacts(parsed.Artifacts)
	return parsed, nil
}

func collectParts(parts []a2aWirePart, text *strings.Builder, artifacts *[]rawArtifact) error {
	for _, part := range parts {
		switch {
		case part.Text != "":
			text.WriteString(part.Text)
		case len(part.Data) > 0:
			text.Write(part.Data)
		case part.File != nil:
			if err := appendWireFile(part.File.Name, part.File.MimeType, part.File.Bytes,
				part.File.URI, part.File.SHA256, artifacts); err != nil {
				return err
			}
		case part.URL != "" || part.Raw != "":
			name := part.Filename
			if name == "" {
				name = "artifact"
			}
			if err := appendWireFile(name, part.MediaType, part.Raw, part.URL, part.SHA256, artifacts); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendWireFile(name, mimeType, rawBase64, uri, sha string, artifacts *[]rawArtifact) error {
	artifact := rawArtifact{AgentArtifact: AgentArtifact{Name: name, MediaType: mimeType, SHA256: strings.ToLower(strings.TrimSpace(sha))}}
	if artifact.Name == "" {
		artifact.Name = "artifact"
	}
	if artifact.MediaType == "" {
		artifact.MediaType = "application/octet-stream"
	}
	if rawBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(rawBase64)
		if err != nil {
			// 兼容 URL-safe base64。
			decoded, err = base64.RawURLEncoding.DecodeString(rawBase64)
		}
		if err != nil {
			return fmt.Errorf("A2A 文件 %s 的 base64 解析失败", artifact.Name)
		}
		if len(decoded) > maxInlineArtifact {
			return fmt.Errorf("A2A 内联文件 %s 超过 %d MiB 上限", artifact.Name, maxInlineArtifact>>20)
		}
		if artifact.SHA256 != "" {
			sum := sha256.Sum256(decoded)
			if hex.EncodeToString(sum[:]) != artifact.SHA256 {
				return fmt.Errorf("A2A 内联文件 %s 的 sha256 不匹配", artifact.Name)
			}
		}
		artifact.Raw = decoded
		artifact.Size = int64(len(decoded))
	}
	if uri != "" {
		artifact.ExternalURI = uri
	}
	if len(artifact.Raw) == 0 && artifact.ExternalURI == "" {
		return nil
	}
	*artifacts = append(*artifacts, artifact)
	return nil
}

func dedupeArtifacts(items []rawArtifact) []rawArtifact {
	seen := map[string]bool{}
	out := make([]rawArtifact, 0, len(items))
	for _, item := range items {
		key := item.ExternalURI
		if key == "" {
			key = fmt.Sprintf("%s|%d", item.Name, len(item.Raw))
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func normalizeTaskState(state string) string {
	value := strings.ToUpper(strings.TrimSpace(state))
	value = strings.TrimPrefix(value, "TASK_STATE_")
	value = strings.ToLower(strings.ReplaceAll(value, "_", "-"))
	if value == "cancelled" {
		value = "canceled"
	}
	return value
}

// ---- v0.3 ----

type a2aCodecV03 struct{}

func (a2aCodecV03) Version() string { return "0.3" }
func (a2aCodecV03) Method() string  { return "message/send" }

func (a2aCodecV03) Message(text string) map[string]any {
	return map[string]any{
		"kind": "message", "role": "user", "messageId": uuid.NewString(),
		"parts": []map[string]any{{"kind": "text", "text": text}},
	}
}

func (a2aCodecV03) OutputModes() []string { return []string{"text/plain", "application/octet-stream"} }
func (a2aCodecV03) Parse(body []byte) (a2aParsed, error) {
	return parseA2AResult(body)
}

// ---- v1.0 ----

type a2aCodecV1 struct{}

func (a2aCodecV1) Version() string { return "1.0" }
func (a2aCodecV1) Method() string  { return "SendMessage" }

func (a2aCodecV1) Message(text string) map[string]any {
	return map[string]any{
		"role": "ROLE_USER", "messageId": uuid.NewString(),
		"parts": []map[string]any{{"text": text}},
	}
}

func (a2aCodecV1) OutputModes() []string { return []string{"text/plain", "application/octet-stream"} }
func (a2aCodecV1) Parse(body []byte) (a2aParsed, error) {
	return parseA2AResult(body)
}
