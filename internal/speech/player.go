package speech

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Player handles audio playback of WAV/PCM data via oto.
type Player struct {
	ctx    *oto.Context
	log    *logger.Logger
	mu     sync.Mutex
	active *oto.Player // currently playing, nil when idle
}

// NewPlayer creates an audio player. Initializes the system audio context.
// Returns an error if the audio device is unavailable.
func NewPlayer(log *logger.Logger) (*Player, error) {
	op := &oto.NewContextOptions{
		SampleRate:   SampleRate,
		ChannelCount: ChannelCount,
		Format:       oto.FormatSignedInt16LE,
	}

	ctx, readyChan, err := oto.NewContext(op)
	if err != nil {
		return nil, err
	}
	<-readyChan

	log.Debug("audio player initialized (rate=%d, channels=%d)", SampleRate, ChannelCount)
	return &Player{ctx: ctx, log: log}, nil
}

// Play plays WAV audio data synchronously. Blocks until playback finishes
// or Stop is called.
func (p *Player) Play(wavData []byte) error {
	pcm, err := extractPCM(wavData)
	if err != nil {
		return err
	}

	player := p.ctx.NewPlayer(bytes.NewReader(pcm))

	p.mu.Lock()
	p.active = player
	p.mu.Unlock()

	player.Play()
	p.log.Debug("audio player: playing %d bytes of PCM", len(pcm))

	// Wait for playback to complete or be interrupted.
	for player.IsPlaying() {
		time.Sleep(10 * time.Millisecond)
	}

	p.mu.Lock()
	p.active = nil
	p.mu.Unlock()

	return player.Close()
}

// Stop interrupts the currently playing audio, if any. Safe to call
// concurrently and when nothing is playing.
func (p *Player) Stop() {
	p.mu.Lock()
	active := p.active
	p.mu.Unlock()

	if active != nil {
		active.Pause()
		p.log.Debug("audio player: interrupted")
	}
}

// extractPCM strips the WAV/RIFF header and returns raw PCM data.
func extractPCM(wav []byte) ([]byte, error) {
	if len(wav) < 44 {
		return nil, errors.New("wav data too short")
	}

	// Verify RIFF header.
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, errors.New("not a valid WAV file")
	}

	// Walk chunks to find the "data" chunk.
	pos := 12
	for pos < len(wav)-8 {
		chunkID := string(wav[pos : pos+4])
		chunkSize := int(binary.LittleEndian.Uint32(wav[pos+4 : pos+8]))

		if chunkID == "data" {
			start := pos + 8
			end := start + chunkSize
			if end > len(wav) {
				end = len(wav)
			}
			return wav[start:end], nil
		}

		pos += 8 + chunkSize
		// Chunks are word-aligned.
		if chunkSize%2 != 0 {
			pos++
		}
	}

	return nil, errors.New("data chunk not found in WAV")
}
