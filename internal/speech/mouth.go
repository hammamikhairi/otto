package speech

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hammamikhairi/ottocook/internal/logger"
)

// MouthOption configures the Mouth.
type MouthOption func(*Mouth)

// WithQueueSize sets the internal notification channel capacity.
func WithQueueSize(n int) MouthOption {
	return func(m *Mouth) {
		m.notify = make(chan struct{}, n)
	}
}

// WithChunkSize sets the approximate max character count per TTS chunk.
// Text longer than this is split at sentence boundaries and synthesized
// in parallel so playback doesn't stall between sentences.
func WithChunkSize(n int) MouthOption {
	return func(m *Mouth) {
		m.chunkSize = n
	}
}

// WithCacheDir sets the filesystem directory used for persistent audio
// caching. If empty, the disk layer is disabled (pure in-memory).
func WithCacheDir(dir string) MouthOption {
	return func(m *Mouth) {
		m.cacheDir = dir
	}
}

// WithDiskWrite controls whether new cache entries are written to disk.
// Even when false, existing on-disk entries are still read.
func WithDiskWrite(enabled bool) MouthOption {
	return func(m *Mouth) {
		m.diskWrite = enabled
	}
}

// Mouth is the central speech dispatcher. It serializes all speech output
// through a single pipeline: queue -> chunk -> synthesize (parallel) -> play
// (sequential). Only one thing speaks at a time. Higher priority items are
// spoken first.
//
// An internal AudioCache transparently avoids re-synthesizing identical text.
// Use Prefetch to pre-warm the cache for text that will be spoken soon.
type Mouth struct {
	tts    *AzureClient
	player *Player
	log    *logger.Logger
	cache  *AudioCache

	mu             sync.Mutex
	queue          []SpeechRequest
	notify         chan struct{}
	speaking       bool
	interrupted    bool   // set by Interrupt(), checked between chunks
	chunkSize      int    // chars per TTS request, 0 = no chunking
	cacheDir       string // filesystem cache directory
	diskWrite      bool   // persist new cache entries to disk
	lastSpokenText string // most recent non-filler text spoken
}

// NewMouth creates a speech dispatcher with the given TTS client and player.
func NewMouth(tts *AzureClient, player *Player, log *logger.Logger, opts ...MouthOption) *Mouth {
	m := &Mouth{
		tts:       tts,
		player:    player,
		log:       log,
		notify:    make(chan struct{}, 32),
		chunkSize: 200,  // sensible default — roughly 2 sentences
		diskWrite: true, // default: persist to disk
	}
	for _, opt := range opts {
		opt(m)
	}
	// Build the cache after options are applied so voice/cacheDir/diskWrite
	// are all settled.
	m.cache = NewAudioCache(tts.Voice(), m.cacheDir, m.diskWrite, log)
	return m
}

// Say queues text to be spoken at the given priority. Non-blocking.
// When something at PriorityNormal or above is queued, any stale
// PriorityLow items are flushed — they're no longer relevant.
func (m *Mouth) Say(text string, priority Priority) {
	m.mu.Lock()
	if priority >= PriorityNormal {
		m.flushLowLocked()
	}
	m.queue = append(m.queue, SpeechRequest{
		Text:     text,
		Priority: priority,
		QueuedAt: time.Now(),
	})
	qLen := len(m.queue)
	m.mu.Unlock()

	m.log.Debug("mouth: queued (priority=%d, queue_len=%d): %s", priority, qLen, truncate(text, 60))

	// Signal the processing goroutine.
	select {
	case m.notify <- struct{}{}:
	default: // already signaled
	}
}

// flushLowLocked removes all PriorityLow items from the queue.
// Must be called with m.mu held.
func (m *Mouth) flushLowLocked() {
	n := 0
	for _, item := range m.queue {
		if item.Priority > PriorityLow {
			m.queue[n] = item
			n++
		}
	}
	dropped := len(m.queue) - n
	m.queue = m.queue[:n]
	if dropped > 0 {
		m.log.Debug("mouth: flushed %d low-priority items", dropped)
	}
}

// IsSpeaking returns true if the mouth is currently synthesizing or playing audio.
func (m *Mouth) IsSpeaking() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.speaking
}

// QueueLen returns the number of pending speech requests.
func (m *Mouth) QueueLen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}

// Interrupt stops the currently playing audio, clears the queue, and
// causes any in-progress multi-chunk playback to abort. Use this when
// something more important needs to be spoken immediately.
func (m *Mouth) Interrupt() {
	m.mu.Lock()
	m.queue = m.queue[:0]
	m.interrupted = true
	m.mu.Unlock()

	// Stop the audio player mid-playback.
	m.player.Stop()

	m.log.Debug("mouth: interrupted — queue cleared, playback stopped")
}

// Start begins the speech processing goroutine. Non-blocking.
func (m *Mouth) Start(ctx context.Context) {
	go m.processLoop(ctx)
	m.log.Info("mouth started")
}

// processLoop waits for queued items and processes them one at a time.
func (m *Mouth) processLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			m.log.Info("mouth stopped")
			return
		case <-m.notify:
			m.drain(ctx)
		}
	}
}

// drain processes all queued items, highest priority first.
func (m *Mouth) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Clear the interrupted flag so we can process new items that
		// were queued after the interrupt.
		m.mu.Lock()
		m.interrupted = false
		m.mu.Unlock()

		item, ok := m.dequeue()
		if !ok {
			return
		}

		m.mu.Lock()
		m.speaking = true
		m.mu.Unlock()

		m.process(ctx, item)

		// Track the last spoken text (skip fillers / very short acks).
		if len(item.Text) > 20 {
			m.mu.Lock()
			m.lastSpokenText = item.Text
			m.mu.Unlock()
		}

		m.mu.Lock()
		m.speaking = false
		m.mu.Unlock()
	}
}

// dequeue removes and returns the highest priority item from the queue.
func (m *Mouth) dequeue() (SpeechRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.queue) == 0 {
		return SpeechRequest{}, false
	}

	bestIdx := 0
	for i, item := range m.queue {
		if item.Priority > m.queue[bestIdx].Priority {
			bestIdx = i
		}
	}

	item := m.queue[bestIdx]
	m.queue = append(m.queue[:bestIdx], m.queue[bestIdx+1:]...)
	return item, true
}

// process synthesizes and plays a single speech request, using chunked
// parallel synthesis for long text.
func (m *Mouth) process(ctx context.Context, req SpeechRequest) {
	waitTime := time.Since(req.QueuedAt).Round(time.Millisecond)
	m.log.Debug("mouth: speaking (priority=%d, waited=%s): %s", req.Priority, waitTime, truncate(req.Text, 60))

	chunks := m.splitChunks(req.Text)
	if len(chunks) <= 1 {
		// Short text — single request, no concurrency overhead.
		m.synthAndPlay(ctx, req.Text)
		return
	}

	m.log.Debug("mouth: split into %d chunks for parallel synthesis", len(chunks))

	// Fire all synthesis requests in parallel, using cache.
	type result struct {
		idx   int
		audio []byte
		err   error
	}
	results := make(chan result, len(chunks))

	for i, chunk := range chunks {
		go func(idx int, text string) {
			audio, err := m.synthesizeWithCache(ctx, text)
			results <- result{idx: idx, audio: audio, err: err}
		}(i, chunk)
	}

	// Collect results into ordered slots.
	audioSlots := make([][]byte, len(chunks))
	for range chunks {
		r := <-results
		if r.err != nil {
			m.log.Error("mouth: chunk %d synthesis failed: %v", r.idx, r.err)
			// Continue — we'll skip the failed chunk during playback.
		} else {
			audioSlots[r.idx] = r.audio
		}
	}

	// Play in order. By now most/all chunks are ready.
	for i, audio := range audioSlots {
		if audio == nil {
			m.log.Debug("mouth: skipping chunk %d (synthesis failed)", i)
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Bail out if interrupted between chunks.
		m.mu.Lock()
		abort := m.interrupted
		m.mu.Unlock()
		if abort {
			m.log.Debug("mouth: aborting chunk playback (interrupted)")
			return
		}
		if err := m.player.Play(audio); err != nil {
			m.log.Error("mouth: chunk %d playback failed: %v", i, err)
		}
	}
}

// synthAndPlay does a single synthesize-then-play for short text.
// Uses the cache to avoid redundant Azure calls.
func (m *Mouth) synthAndPlay(ctx context.Context, text string) {
	audioData, err := m.synthesizeWithCache(ctx, text)
	if err != nil {
		m.log.Error("mouth: synthesis failed: %v", err)
		return
	}
	if err := m.player.Play(audioData); err != nil {
		m.log.Error("mouth: playback failed: %v", err)
	}
}

// synthesizeWithCache checks the cache first, otherwise calls Azure and
// stores the result. Thread-safe.
func (m *Mouth) synthesizeWithCache(ctx context.Context, text string) ([]byte, error) {
	if audio, ok := m.cache.Get(text); ok {
		return audio, nil
	}
	audio, err := m.tts.Synthesize(ctx, text)
	if err != nil {
		return nil, err
	}
	m.cache.Put(text, audio)
	return audio, nil
}

// splitChunks breaks text into sentence-boundary chunks of approximately
// m.chunkSize characters. If chunkSize is 0 or the text is short, it
// returns the text as-is in a single slice.
func (m *Mouth) splitChunks(text string) []string {
	if m.chunkSize <= 0 || len(text) <= m.chunkSize {
		return []string{text}
	}

	// Split on sentence-ending punctuation followed by whitespace.
	sentences := splitSentences(text)

	var chunks []string
	var current strings.Builder

	for _, s := range sentences {
		// If adding this sentence would exceed the limit, flush.
		if current.Len() > 0 && current.Len()+len(s) > m.chunkSize {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
		current.WriteString(s)
	}
	if current.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(current.String()))
	}

	// Filter out empties.
	var out []string
	for _, c := range chunks {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// splitSentences splits text at sentence boundaries (. ! ?) keeping the
// punctuation attached to the preceding sentence.
func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		current.WriteRune(runes[i])
		if isSentenceEnd(runes[i]) {
			// Consume trailing whitespace and include it.
			for i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
				i++
				current.WriteRune(runes[i])
			}
			sentences = append(sentences, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		sentences = append(sentences, current.String())
	}
	return sentences
}

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

// truncate shortens a string for logging.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// ── Prefetching / Cache ──────────────────────────────────────────

// Prefetch pre-synthesizes the given texts in background goroutines and
// stores the results in the audio cache. It skips texts that are already
// cached. Non-blocking — launches goroutines and returns immediately.
//
// Call it any time you know what text will be spoken next (e.g. the next
// cooking step) so playback starts instantly when Say is called.
func (m *Mouth) Prefetch(ctx context.Context, texts ...string) {
	for _, text := range texts {
		if text == "" {
			continue
		}

		// For long text, split into the same chunks Say would use.
		chunks := m.splitChunks(text)
		for _, chunk := range chunks {
			if m.cache.Has(chunk) {
				m.log.Debug("prefetch: already cached: %s", truncate(chunk, 50))
				continue
			}
			go func(t string) {
				m.log.Debug("prefetch: synthesizing: %s", truncate(t, 50))
				audio, err := m.tts.Synthesize(ctx, t)
				if err != nil {
					m.log.Error("prefetch: synthesis failed: %v", err)
					return
				}
				m.cache.Put(t, audio)
				m.log.Debug("prefetch: cached %d bytes for: %s", len(audio), truncate(t, 50))
			}(chunk)
		}
	}
}

// LastSpoken returns the most recently spoken non-filler text.
func (m *Mouth) LastSpoken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastSpokenText
}

// Cache returns the audio cache used by this Mouth. Useful for stats/logging.
func (m *Mouth) Cache() *AudioCache { return m.cache }
