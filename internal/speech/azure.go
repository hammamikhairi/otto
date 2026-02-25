package speech

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hammamikhairi/ottocook/internal/logger"
)

// AzureOption configures the Azure TTS client.
type AzureOption func(*AzureClient)

// WithVoice sets the TTS voice.
func WithVoice(voice string) AzureOption {
	return func(c *AzureClient) {
		c.voice = voice
	}
}

// WithAudioFormat sets the audio output format.
func WithAudioFormat(format string) AzureOption {
	return func(c *AzureClient) {
		c.format = format
	}
}

// WithHTTPTimeout sets the HTTP client timeout for TTS requests.
func WithHTTPTimeout(d time.Duration) AzureOption {
	return func(c *AzureClient) {
		c.httpClient.Timeout = d
	}
}

// AzureClient handles text-to-speech synthesis via Azure Cognitive Services.
type AzureClient struct {
	subscriptionKey string
	region          string
	voice           string
	format          string
	httpClient      *http.Client
	log             *logger.Logger
}

// Voice returns the configured voice name.
func (c *AzureClient) Voice() string { return c.voice }

// NewAzureClient creates an Azure TTS client with the given credentials.
func NewAzureClient(key, region string, log *logger.Logger, opts ...AzureOption) *AzureClient {
	c := &AzureClient{
		subscriptionKey: key,
		region:          region,
		voice:           DefaultVoice,
		format:          DefaultAudioFormat,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		log: log,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Synthesize converts text to speech audio data (WAV bytes).
func (c *AzureClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	url := fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/v1", c.region)

	ssml := c.buildSSML(text)
	c.log.Debug("azure tts: synthesizing %d chars with voice %s", len(text), c.voice)

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(ssml))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Ocp-Apim-Subscription-Key", c.subscriptionKey)
	req.Header.Set("Content-Type", "application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", c.format)
	req.Header.Set("User-Agent", "OttoCook/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("azure tts error %d: %s", resp.StatusCode, string(body))
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading audio data: %w", err)
	}

	c.log.Debug("azure tts: got %d bytes of audio", len(audioData))
	return audioData, nil
}

// buildSSML creates SSML markup for the synthesis request.
func (c *AzureClient) buildSSML(text string) string {
	return fmt.Sprintf(
		`<speak version='1.0' xml:lang='en-US'><voice xml:lang='en-US' name='%s'>%s</voice></speak>`,
		c.voice, text,
	)
}
