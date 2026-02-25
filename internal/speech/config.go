package speech

import "time"

// Default voice for TTS. Change this constant to switch voices.
// Full list: https://learn.microsoft.com/en-us/azure/ai-services/speech-service/language-support
const DefaultVoice = "en-US-AvaNeural"

// Audio format returned by Azure and expected by the player.
const DefaultAudioFormat = "riff-24khz-16bit-mono-pcm"

// Audio parameters matching the default format.
const (
	SampleRate   = 24000
	ChannelCount = 1
	BitDepth     = 16
)

// Env var names for Azure Speech credentials.
const (
	EnvAzureSpeechKey    = "AZURE_SPEECH_KEY"
	EnvAzureSpeechRegion = "AZURE_SPEECH_REGION"
)

// Priority levels for speech requests. Higher value = speaks first.
type Priority int

const (
	PriorityLow      Priority = iota // watcher comments, idle chatter
	PriorityNormal                   // step instructions, info
	PriorityHigh                     // timer notifications
	PriorityCritical                 // urgent alerts, errors
)

// SpeechRequest is a queued item waiting to be spoken.
type SpeechRequest struct {
	Text     string
	Priority Priority
	QueuedAt time.Time
}
