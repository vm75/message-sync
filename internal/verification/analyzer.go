package verification

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type AnalysisInput struct {
	DomainClass   string
	LinkedInValid bool
	EvidenceType  string
	Evidence      []byte
}
type AnalysisResult struct {
	Status     string `json:"status"`
	Confidence string `json:"confidence"`
	Assessment string `json:"assessment"`
}
type Analyzer interface {
	Analyze(context.Context, AnalysisInput) (AnalysisResult, error)
}
type OpenRouterAnalyzer struct {
	APIKey, Model string
	Client        *http.Client
}

func NewOpenRouterFromEnv() *OpenRouterAnalyzer {
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" || strings.TrimSpace(os.Getenv("OPENROUTER_ALLOW_TRAINING")) != "false" {
		return nil
	}
	model := strings.TrimSpace(os.Getenv("OPENROUTER_MODEL"))
	if model == "" {
		model = "openrouter/free"
	}
	return &OpenRouterAnalyzer{APIKey: key, Model: model, Client: &http.Client{Timeout: 30 * time.Second}}
}
func (a *OpenRouterAnalyzer) Analyze(ctx context.Context, in AnalysisInput) (AnalysisResult, error) {
	if a == nil || a.APIKey == "" {
		return AnalysisResult{Status: "unavailable"}, errors.New("analyzer unavailable")
	}
	if len(in.Evidence) == 0 {
		return AnalysisResult{Status: "unavailable"}, errors.New("evidence is required")
	}
	content := map[string]any{"type": "text", "text": "Return JSON only with status, confidence, and short assessment. This is advisory document review, not identity proof."}
	if strings.HasPrefix(in.EvidenceType, "image/") {
		content = map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + in.EvidenceType + ";base64," + base64.StdEncoding.EncodeToString(in.Evidence)}}
	}
	payload := map[string]any{"model": a.Model, "messages": []any{map[string]any{"role": "user", "content": []any{content}}}, "response_format": map[string]string{"type": "json_object"}}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AnalysisResult{Status: "failed"}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "message-sync advisory review")
	resp, err := a.Client.Do(req)
	if err != nil {
		return AnalysisResult{Status: "unavailable"}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		return AnalysisResult{Status: "unavailable"}, errors.New("analysis rate limited")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AnalysisResult{Status: "failed"}, errors.New("analysis provider rejected request")
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil || len(envelope.Choices) != 1 {
		return AnalysisResult{Status: "failed"}, errors.New("invalid analysis response")
	}
	var result AnalysisResult
	dec := json.NewDecoder(strings.NewReader(envelope.Choices[0].Message.Content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil || result.Status != "complete" || (result.Confidence != "HIGH" && result.Confidence != "MEDIUM" && result.Confidence != "LOW" && result.Confidence != "UNVERIFIED") {
		return AnalysisResult{Status: "failed"}, fmt.Errorf("invalid structured analysis")
	}
	if len(result.Assessment) > 500 {
		return AnalysisResult{Status: "failed"}, errors.New("analysis is too long")
	}
	return result, nil
}

func AnalyzeAsync(ctx context.Context, a Analyzer, in AnalysisInput, done func(AnalysisResult)) {
	if a == nil || done == nil {
		return
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		result, err := a.Analyze(jobCtx, in)
		if err != nil && result.Status == "" {
			result.Status = "unavailable"
		}
		done(result)
	}()
}
