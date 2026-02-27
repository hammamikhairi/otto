// Package wakeword provides real-time wake-word detection using the
// openWakeWord ONNX pipeline: melspectrogram → embedding → wakeword.
//
// The detector opens a single audio capture device via miniaudio (malgo),
// feeds 80 ms chunks through three ONNX models, and fires a callback
// when the wakeword score exceeds a threshold.
//
// All model files (melspectrogram.onnx, embedding_model.onnx, <wakeword>.onnx)
// and the ONNX Runtime shared library must be provided at construction time.
package wakeword

import (
	"context"
	"encoding/binary"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
	"github.com/hammamikhairi/ottocook/internal/logger"
	ort "github.com/yalue/onnxruntime_go"
)

// ── Constants matching the openWakeWord pipeline ─────────────────

const (
	sampleRate    = 16000
	chunkSamples  = 1280 // 80 ms @ 16 kHz
	audioQueueCap = 32
	melWindowSize = 76 // embedding model needs 76 mel frames
	melStepSize   = 8  // step between embedding windows
	embeddingDim  = 96 // output dim per embedding frame
	nEmbedFrames  = 16 // wakeword model needs 16 embedding frames
	melBins       = 32 // melspectrogram output bands
	nMelFrames    = 5  // 1280 samples → 5 mel frames

	// scoreWindowSize is the number of recent scores to track.
	// The detector triggers when the max score in this window
	// exceeds the threshold.  This compensates for frame-alignment
	// variance — the peak may arrive one frame before or after
	// the "ideal" position.  5 frames ≈ 400 ms.
	scoreWindowSize = 5

	// recentWindow is how many of the most recent embedding slots to
	// actually pass to the wakeword model.  The rest are zeroed.
	// This mimics the initial-launch state where the model saw
	// [0,0,...,0, speech, speech] and scored 0.8+.  Silence embeddings
	// in older slots can never accumulate and suppress detection because
	// they're always masked to zero at scoring time.
	recentWindow = 5 // ~400 ms of context (5 × 80 ms embed steps)
)

// Config holds the paths and tuning knobs for a Detector.
type Config struct {
	// Model paths (required).
	WakewordModel  string // e.g. "models/hey_otto.onnx"
	MelspecModel   string // e.g. "bin/melspectrogram.onnx"
	EmbeddingModel string // e.g. "bin/embedding_model.onnx"
	OnnxLib        string // e.g. "bin/libonnxruntime.dylib"

	// Detection tuning.
	Threshold float64       // score ≥ threshold → detected (default 0.5)
	Cooldown  time.Duration // min time between detections (default 1.5 s)
}

func (c *Config) defaults() {
	if c.Threshold <= 0 {
		c.Threshold = 0.3
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 1500 * time.Millisecond
	}
}

// Detector listens for a wakeword continuously and fires OnDetected.
type Detector struct {
	cfg Config
	log *logger.Logger

	// Callback fired (from the processing goroutine) when the wakeword
	// is detected.  Set before calling Start.
	OnDetected func()

	mu         sync.Mutex
	paused     bool
	needsReset bool // set on Resume to flush stale pipeline state
}

// New creates a Detector.  Call Start to begin listening.
func New(cfg Config, log *logger.Logger) *Detector {
	cfg.defaults()
	return &Detector{cfg: cfg, log: log}
}

// Pause temporarily stops detecting (e.g. while TTS is playing so we
// don't pick up the speaker output).
func (d *Detector) Pause() {
	d.mu.Lock()
	d.paused = true
	d.mu.Unlock()
}

// Resume re-enables detection after a Pause.
func (d *Detector) Resume() {
	d.mu.Lock()
	d.paused = false
	d.needsReset = true
	d.mu.Unlock()
}

func (d *Detector) isPaused() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.paused
}

// checkReset returns true (once) if Resume was called, signaling the
// processing loop to flush all stale pipeline buffers.
func (d *Detector) checkReset() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.needsReset {
		d.needsReset = false
		return true
	}
	return false
}

// Start initialises the ONNX models and the audio capture device,
// then processes audio in a blocking loop until ctx is cancelled.
// Run this in its own goroutine.
func (d *Detector) Start(ctx context.Context) error {
	// ── ONNX Runtime ────────────────────────────────────────────
	d.log.Debug("wakeword: initializing ONNX runtime (lib=%s)", d.cfg.OnnxLib)
	ort.SetSharedLibraryPath(d.cfg.OnnxLib)
	if err := ort.InitializeEnvironment(); err != nil {
		d.log.Error("wakeword: ONNX init failed: %v", err)
		return err
	}
	defer ort.DestroyEnvironment()
	d.log.Debug("wakeword: ONNX runtime initialized")

	// ── Melspectrogram model ────────────────────────────────────
	melspecIn, err := ort.NewEmptyTensor[float32](ort.NewShape(1, chunkSamples))
	if err != nil {
		return err
	}
	defer melspecIn.Destroy()

	melspecOut, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 1, nMelFrames, melBins))
	if err != nil {
		return err
	}
	defer melspecOut.Destroy()

	msInInfo, msOutInfo, err := ort.GetInputOutputInfo(d.cfg.MelspecModel)
	if err != nil {
		return err
	}
	melspecSess, err := ort.NewAdvancedSession(
		d.cfg.MelspecModel,
		[]string{msInInfo[0].Name}, []string{msOutInfo[0].Name},
		[]ort.Value{melspecIn}, []ort.Value{melspecOut},
		nil,
	)
	if err != nil {
		return err
	}
	defer melspecSess.Destroy()

	// ── Embedding model ─────────────────────────────────────────
	embedIn, err := ort.NewEmptyTensor[float32](ort.NewShape(1, melWindowSize, melBins, 1))
	if err != nil {
		return err
	}
	defer embedIn.Destroy()

	embedOut, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 1, 1, embeddingDim))
	if err != nil {
		return err
	}
	defer embedOut.Destroy()

	emInInfo, emOutInfo, err := ort.GetInputOutputInfo(d.cfg.EmbeddingModel)
	if err != nil {
		return err
	}
	embedSess, err := ort.NewAdvancedSession(
		d.cfg.EmbeddingModel,
		[]string{emInInfo[0].Name}, []string{emOutInfo[0].Name},
		[]ort.Value{embedIn}, []ort.Value{embedOut},
		nil,
	)
	if err != nil {
		return err
	}
	defer embedSess.Destroy()

	// ── Wakeword model ──────────────────────────────────────────
	wwIn, err := ort.NewEmptyTensor[float32](ort.NewShape(1, nEmbedFrames, embeddingDim))
	if err != nil {
		return err
	}
	defer wwIn.Destroy()

	wwOut, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 1))
	if err != nil {
		return err
	}
	defer wwOut.Destroy()

	wwInInfo, wwOutInfo, err := ort.GetInputOutputInfo(d.cfg.WakewordModel)
	if err != nil {
		return err
	}
	wwSess, err := ort.NewAdvancedSession(
		d.cfg.WakewordModel,
		[]string{wwInInfo[0].Name}, []string{wwOutInfo[0].Name},
		[]ort.Value{wwIn}, []ort.Value{wwOut},
		nil,
	)
	if err != nil {
		return err
	}
	defer wwSess.Destroy()

	// ── Audio capture via miniaudio ─────────────────────────────
	mCtx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(_ string) {})
	if err != nil {
		return err
	}
	defer func() { _ = mCtx.Uninit(); mCtx.Free() }()

	devCfg := malgo.DefaultDeviceConfig(malgo.Capture)
	devCfg.SampleRate = sampleRate
	devCfg.Capture.Format = malgo.FormatS16
	devCfg.Capture.Channels = 1
	devCfg.Alsa.NoMMap = 1

	audioCh := make(chan []int16, audioQueueCap)
	var audioDrops atomic.Int64

	callbacks := malgo.DeviceCallbacks{
		Data: func(_ []byte, raw []byte, _ uint32) {
			if len(raw) == 0 {
				return
			}
			n := len(raw) / 2
			pcm := make([]int16, n)
			for i := 0; i < n; i++ {
				pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2 : i*2+2]))
			}
			select {
			case audioCh <- pcm:
			default:
				audioDrops.Add(1)
			}
		},
	}

	device, err := malgo.InitDevice(mCtx.Context, devCfg, callbacks)
	if err != nil {
		return err
	}
	defer device.Uninit()

	if err := device.Start(); err != nil {
		d.log.Error("wakeword: audio device start failed: %v", err)
		return err
	}
	defer device.Stop()
	d.log.Debug("wakeword: audio capture started (rate=%d, chunk=%d)", sampleRate, chunkSamples)

	chunksProcessed := 0

	// ── Processing state ────────────────────────────────────────
	melBuffer := make([]float32, 0, 300*melBins)
	embedBuffer := make([]float32, nEmbedFrames*embeddingDim)
	audioRem := make([]int16, 0, chunkSamples*2)
	lastDetect := time.Time{}

	// Trailing score window — trigger on max within the window.
	scoreWindow := make([]float32, scoreWindowSize)
	scoreIdx := 0

	// Diagnostic counters.
	var (
		peakScore     float32
		totalEmbeds   int
		lastStatsDump = time.Now()
		statInterval  = 2 * time.Second
	)

	// (no extra state needed — zero-padded scoring handles silence automatically)

	// ── Main loop ───────────────────────────────────────────────
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case frame := <-audioCh:
			if d.isPaused() {
				continue
			}

			// After a Pause/Resume cycle, flush all pipeline state so
			// stale mel frames and embeddings don't pollute scoring.
			if d.checkReset() {
				melBuffer = melBuffer[:0]
				for i := range embedBuffer {
					embedBuffer[i] = 0
				}
				audioRem = audioRem[:0]
				for i := range scoreWindow {
					scoreWindow[i] = 0
				}
				scoreIdx = 0
				peakScore = 0
				totalEmbeds = 0
				d.log.Debug("wakeword: pipeline buffers reset after resume")
			}

			chunksProcessed++

			// ── Periodic state dump (every 5s) ──────────────────
			if now := time.Now(); now.Sub(lastStatsDump) >= statInterval {
				drops := audioDrops.Load()
				d.log.Debug("wakeword: [STATS] chunks=%d embeds=%d drops=%d melBuf=%d/%d(cap) audioRem=%d/%d(cap) peakScore=%.4f paused=%v",
					chunksProcessed, totalEmbeds, drops,
					len(melBuffer)/melBins, cap(melBuffer)/melBins,
					len(audioRem), cap(audioRem),
					peakScore, d.isPaused())
				peakScore = 0
				lastStatsDump = now
			}

			audioRem = append(audioRem, frame...)

			for len(audioRem) >= chunkSamples {
				chunk := audioRem[:chunkSamples]
				// Compact: copy remaining to front of slice to release old backing memory.
				n := copy(audioRem, audioRem[chunkSamples:])
				audioRem = audioRem[:n]

				// ── Step 1: melspectrogram ───────────────────────
				inData := melspecIn.GetData()
				var sumSq float64
				for i, v := range chunk {
					inData[i] = float32(v)
					sumSq += float64(v) * float64(v)
				}
				rms := math.Sqrt(sumSq / float64(len(chunk)))
				_ = rms // kept for diagnostics

				if err := melspecSess.Run(); err != nil {
					d.log.Error("wakeword: melspec run failed: %v", err)
					continue
				}

				melData := melspecOut.GetData()
				for f := 0; f < nMelFrames; f++ {
					for b := 0; b < melBins; b++ {
						idx := f*melBins + b
						if idx < len(melData) {
							melBuffer = append(melBuffer, melData[idx]/10.0+2.0)
						}
					}
				}

				// ── Step 2: embedding ───────────────────────────
				totalMel := len(melBuffer) / melBins
				newEmbed := false

				for totalMel >= melWindowSize {
					eData := embedIn.GetData()
					for i := 0; i < melWindowSize*melBins; i++ {
						eData[i] = melBuffer[i]
					}
					if err := embedSess.Run(); err != nil {
						d.log.Error("wakeword: embed run failed: %v", err)
						break
					}
					eOut := embedOut.GetData()

					// Normal sliding window: shift left, insert at end.
					copy(embedBuffer, embedBuffer[embeddingDim:])
					copy(embedBuffer[(nEmbedFrames-1)*embeddingDim:], eOut[:embeddingDim])
					newEmbed = true

					// Compact melBuffer: copy remaining to front to prevent
					// unbounded backing-array growth from reslicing.
					n := copy(melBuffer, melBuffer[melStepSize*melBins:])
					melBuffer = melBuffer[:n]
					totalMel = len(melBuffer) / melBins
				}

				// Trim excess mel history (compact, not reslice).
				if totalMel > melWindowSize {
					excess := (totalMel - melWindowSize) * melBins
					n := copy(melBuffer, melBuffer[excess:])
					melBuffer = melBuffer[:n]
				}

				if !newEmbed {
					continue
				}

				totalEmbeds++

				// ── Step 3: wakeword scoring ────────────────────
				// Feed the model a zero-padded buffer: only the last
				// `recentWindow` embedding slots are real; the rest are
				// zeros.  This permanently mimics the fresh-launch state
				// where the model scores 0.8+ and prevents silence
				// embeddings from ever suppressing detection.
				//
				// this will be our dirty little secret :)
				wwData := wwIn.GetData()
				padSlots := nEmbedFrames - recentWindow
				for i := 0; i < padSlots*embeddingDim; i++ {
					wwData[i] = 0
				}
				copy(wwData[padSlots*embeddingDim:], embedBuffer[padSlots*embeddingDim:])
				if err := wwSess.Run(); err != nil {
					d.log.Error("wakeword: ww run failed: %v", err)
					continue
				}

				score := wwOut.GetData()[0]
				now := time.Now()

				if score > peakScore {
					peakScore = score
				}

				// Insert into trailing score window.
				scoreWindow[scoreIdx%scoreWindowSize] = score
				scoreIdx++

				// Compute the max score in the window.
				var maxScore float32
				for _, s := range scoreWindow {
					if s > maxScore {
						maxScore = s
					}
				}

				// Log score when it's interesting (above 10% of threshold)
				// or at low frequency for ambient baseline.
				if float64(maxScore) >= d.cfg.Threshold*0.1 {
					d.log.Debug("wakeword: score=%.6f max=%.6f (threshold=%.2f)", score, maxScore, d.cfg.Threshold)
				}

				if float64(maxScore) >= d.cfg.Threshold && now.Sub(lastDetect) > d.cfg.Cooldown {
					d.log.Info("wakeword: DETECTED (score=%.4f, windowMax=%.4f)", score, maxScore)
					lastDetect = now
					// Clear window so we don't re-trigger on the same peak.
					for i := range scoreWindow {
						scoreWindow[i] = 0
					}
					if d.OnDetected != nil {
						d.OnDetected()
					}
				}
			}
		}
	}
}
