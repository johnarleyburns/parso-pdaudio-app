// Package enrich uses the DeepSeek API (OpenAI-compatible) to extract and
// validate structured classical-music metadata (composer, work, movement) from
// the messy per-source title strings, and to correct mis-attributed composers.
//
// The API key is a secret: it is only read at runtime (see LoadAPIKey) and is
// never logged or embedded in source.
package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultModel is the bulk model (DeepSeek-V3): a good accuracy/cost balance for
// thousands of short extraction calls. Escalate low-confidence rows to
// EscalationModel (DeepSeek-R1, the reasoning model).
const (
	DefaultModel    = "deepseek-chat"
	EscalationModel = "deepseek-reasoner"
	apiURL          = "https://api.deepseek.com/chat/completions"
)

// LoadAPIKey returns the DeepSeek API key from $DEEPSEEK_API_KEY, else from
// ~/.deepseek-api-key. The value is never logged.
func LoadAPIKey() (string, error) {
	if k := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")); k != "" {
		return k, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if data, rerr := os.ReadFile(filepath.Join(home, ".deepseek-api-key")); rerr == nil {
			if k := strings.TrimSpace(string(data)); k != "" {
				return k, nil
			}
		}
	}
	return "", fmt.Errorf("no DeepSeek API key (set DEEPSEEK_API_KEY or ~/.deepseek-api-key)")
}

// Input is the raw per-track context handed to the model.
type Input struct {
	Title      string
	Source     string
	SourceItem string
	Composer   string // source-derived composer (may be wrong)
	Performer  string
	Album      string
}

// Result is the structured metadata the model returns.
type Result struct {
	ComposerCanonical string  `json:"composer_canonical"`
	WorkTitle         string  `json:"work_title"`
	Catalog           string  `json:"catalog"`
	MovementIndex     int     `json:"movement_index"`
	MovementTitle     string  `json:"movement_title"`
	ComposerCorrected bool    `json:"composer_corrected"`
	CorrectionReason  string  `json:"composer_correction_reason"`
	Confidence        float64 `json:"confidence"`
	Model             string  `json:"-"`
}

// Client calls the DeepSeek chat-completions API.
type Client struct {
	APIKey string
	Model  string
	HTTP   *http.Client
}

// NewClient builds a client from an API key. Model defaults to DefaultModel.
func NewClient(apiKey, model string) *Client {
	if model == "" {
		model = DefaultModel
	}
	return &Client{APIKey: apiKey, Model: model, HTTP: &http.Client{Timeout: 120 * time.Second}}
}

const systemPrompt = `You normalize public-domain classical music metadata. Given a messy recording title and its source-provided fields, extract the canonical composer, work, catalog number, and movement.

Critically: the source-provided composer is often WRONG (e.g. a march or pops recording mis-filed under a classical composer's category). Judge whether the provided composer actually matches the work named in the title. If it does not, set composer_corrected=true and return the correct composer.

Respond with ONLY a JSON object (no prose, no code fences) with exactly these keys:
{
  "composer_canonical": string,   // canonical full name e.g. "Ludwig van Beethoven"; "" if genuinely unknown
  "work_title": string,           // work title without movement e.g. "Symphony No. 5 in C minor"
  "catalog": string,              // e.g. "Op. 67", "BWV 846", "K. 550"; "" if none; never guess
  "movement_index": integer,      // 1-based movement number, or 0 if single-movement/unknown
  "movement_title": string,       // e.g. "I. Allegro con brio"; "" if not a movement
  "composer_corrected": boolean,  // true if you overrode a wrong source composer
  "composer_correction_reason": string, // brief reason when corrected; else ""
  "confidence": number            // 0.0-1.0 confidence in composer and work
}
Use empty strings for fields you cannot determine.`

type apiRequest struct {
	Model          string       `json:"model"`
	Messages       []apiMessage `json:"messages"`
	ResponseFormat *respFormat  `json:"response_format,omitempty"`
	MaxTokens      int          `json:"max_tokens"`
	Temperature    float64      `json:"temperature"`
	Stream         bool         `json:"stream"`
}

type respFormat struct {
	Type string `json:"type"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type apiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Enrich calls the model for one track and returns structured metadata.
func (c *Client) Enrich(ctx context.Context, in Input) (Result, error) {
	reqBody := apiRequest{
		Model: c.Model,
		Messages: []apiMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: renderPrompt(in)},
		},
		MaxTokens:   1024,
		Temperature: 0,
	}
	// deepseek-reasoner does not support response_format json_object; the schema
	// in the system prompt is sufficient there.
	if c.Model != EscalationModel {
		reqBody.ResponseFormat = &respFormat{Type: "json_object"}
	}
	buf, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(buf))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return Result{}, &retryableError{status: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("deepseek http %d: %s", resp.StatusCode, safeErr(body))
	}
	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return Result{}, fmt.Errorf("decode response: %w", err)
	}
	if ar.Error != nil {
		return Result{}, fmt.Errorf("deepseek %s: %s", ar.Error.Type, ar.Error.Message)
	}
	if len(ar.Choices) == 0 {
		return Result{}, fmt.Errorf("no choices in response")
	}
	var r Result
	if err := json.Unmarshal([]byte(extractJSON(ar.Choices[0].Message.Content)), &r); err != nil {
		return Result{}, fmt.Errorf("decode content json: %w", err)
	}
	r.Model = c.Model
	return r, nil
}

// extractJSON strips optional ``` fences / surrounding prose and returns the
// first JSON object found (deepseek-reasoner may wrap output).
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func renderPrompt(in Input) string {
	var b bytes.Buffer
	b.WriteString("Title: " + in.Title + "\n")
	if in.Composer != "" {
		b.WriteString("Source-provided composer (may be wrong): " + in.Composer + "\n")
	}
	if in.Performer != "" {
		b.WriteString("Performer: " + in.Performer + "\n")
	}
	if in.Album != "" {
		b.WriteString("Album: " + in.Album + "\n")
	}
	if in.SourceItem != "" {
		b.WriteString("Source item: " + in.SourceItem + "\n")
	}
	b.WriteString("Source: " + in.Source + "\n")
	return b.String()
}

// safeErr truncates an API error body so we never spill large/sensitive payloads
// into logs (the request Authorization header is never echoed by the API).
func safeErr(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

type retryableError struct {
	status int
}

func (e *retryableError) Error() string {
	return fmt.Sprintf("retryable http %d", e.status)
}

// IsRetryable reports whether err is a transient API error worth retrying.
func IsRetryable(err error) bool {
	_, ok := err.(*retryableError)
	return ok
}
