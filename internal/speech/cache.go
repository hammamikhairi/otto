package speech

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"github.com/hammamikhairi/ottocook/internal/logger"
)

// AudioCache is a thread-safe two-tier cache (in-memory + filesystem) for
// synthesized audio. The cache key is sha256(voice + ":" + text) so a voice
// change automatically causes cache misses until the voice is switched back.
//
// Disk behaviour is controlled by diskWrite:
//
//	diskWrite=true  -> reads from mem, then disk; writes to both.
//	diskWrite=false -> reads from mem, then disk; writes to mem only.
//
// This means the on-disk cache is always consulted, even when writes are
// disabled, giving the user a warm start from previous runs.
type AudioCache struct {
	mu        sync.RWMutex
	entries   map[string][]byte // hash -> WAV bytes
	log       *logger.Logger
	voice     string // included in every cache key
	cacheDir  string // filesystem cache directory (empty = no disk layer)
	diskWrite bool   // whether to persist new entries to disk
	hits      int64
	misses    int64
}

// NewAudioCache creates an audio cache.
//
//   - voice:     the TTS voice name baked into every cache key.
//   - cacheDir:  path to the on-disk cache directory. If empty, the disk
//     layer is disabled entirely (pure in-memory).
//   - diskWrite: when true, new entries are written to cacheDir. When false,
//     existing files in cacheDir are still read, but nothing new is persisted.
func NewAudioCache(voice, cacheDir string, diskWrite bool, log *logger.Logger) *AudioCache {
	c := &AudioCache{
		entries:   make(map[string][]byte),
		log:       log,
		voice:     voice,
		cacheDir:  cacheDir,
		diskWrite: diskWrite,
	}

	// Ensure the cache directory exists when disk writes are enabled.
	if cacheDir != "" && diskWrite {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			log.Error("cache: failed to create cache dir %s: %v", cacheDir, err)
		}
	}

	return c
}

// Get returns cached audio for the given text and true, or nil and false.
// It checks the in-memory map first, then falls back to the disk cache.
func (c *AudioCache) Get(text string) ([]byte, bool) {
	key := c.hashKey(text)

	// 1. In-memory lookup.
	c.mu.RLock()
	data, ok := c.entries[key]
	c.mu.RUnlock()

	if ok {
		c.mu.Lock()
		c.hits++
		c.mu.Unlock()
		c.log.Debug("cache hit (mem): %s (%d bytes)", truncateForLog(text, 40), len(data))
		return data, true
	}

	// 2. Disk lookup.
	if c.cacheDir != "" {
		if diskData, diskOK := c.readDisk(key); diskOK {
			// Promote to in-memory for faster subsequent hits.
			c.mu.Lock()
			c.entries[key] = diskData
			c.hits++
			c.mu.Unlock()
			c.log.Debug("cache hit (disk): %s (%d bytes)", truncateForLog(text, 40), len(diskData))
			return diskData, true
		}
	}

	c.mu.Lock()
	c.misses++
	c.mu.Unlock()
	return nil, false
}

// Put stores audio data for the given text. Always writes to memory; writes
// to disk only when diskWrite is enabled.
func (c *AudioCache) Put(text string, audio []byte) {
	key := c.hashKey(text)

	c.mu.Lock()
	c.entries[key] = audio
	size := len(c.entries)
	c.mu.Unlock()

	c.log.Debug("cache store (mem): %s (%d bytes, %d entries)", truncateForLog(text, 40), len(audio), size)

	if c.cacheDir != "" && c.diskWrite {
		c.writeDisk(key, audio)
	}
}

// Has returns true if audio for the text is cached (memory or disk).
func (c *AudioCache) Has(text string) bool {
	key := c.hashKey(text)

	c.mu.RLock()
	_, ok := c.entries[key]
	c.mu.RUnlock()
	if ok {
		return true
	}

	if c.cacheDir != "" {
		return c.existsOnDisk(key)
	}
	return false
}

// Len returns the number of in-memory cached entries.
func (c *AudioCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Stats returns hit and miss counts.
func (c *AudioCache) Stats() (hits, misses int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses
}

// Clear empties the in-memory cache. The disk cache is NOT cleared.
func (c *AudioCache) Clear() {
	c.mu.Lock()
	c.entries = make(map[string][]byte)
	c.hits = 0
	c.misses = 0
	c.mu.Unlock()
	c.log.Debug("cache cleared (mem)")
}

// ── hashing ──────────────────────────────────────────────────────

// hashKey returns a hex-encoded SHA-256 of voice + ":" + text.
func (c *AudioCache) hashKey(text string) string {
	h := sha256.Sum256([]byte(c.voice + ":" + text))
	return hex.EncodeToString(h[:])
}

// ── disk helpers ─────────────────────────────────────────────────

func (c *AudioCache) diskPath(key string) string {
	return filepath.Join(c.cacheDir, key+".wav")
}

func (c *AudioCache) readDisk(key string) ([]byte, bool) {
	data, err := os.ReadFile(c.diskPath(key))
	if err != nil {
		return nil, false
	}
	return data, true
}

func (c *AudioCache) writeDisk(key string, audio []byte) {
	path := c.diskPath(key)
	if err := os.WriteFile(path, audio, 0o644); err != nil {
		c.log.Error("cache: disk write failed for %s: %v", path, err)
	} else {
		c.log.Debug("cache store (disk): %s (%d bytes)", key[:12], len(audio))
	}
}

func (c *AudioCache) existsOnDisk(key string) bool {
	_, err := os.Stat(c.diskPath(key))
	return err == nil
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
