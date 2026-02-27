# OttoCook

A conversational cooking assistant that lives in your terminal. It talks you through recipes step by step, manages your timers, answers your dumb questions mid-cook, and yells at you when something's about to burn.

Built in Go. Runs locally. Talks back.

![banner](img/banner.png)

## Why this exists

I burned my dinner once. Not a little - fully ruined it. Went to bed hungry that night because I got distracted, missed a timer, and let everything go sideways.

So I built a thing that wouldn't let that happen again. OttoCook sits in your terminal, walks you through the recipe, keeps track of every timer, and won't shut up until you acknowledge that your food is done. It's the kitchen buddy I needed.

The first thing I tried cooking with it was Chicken Alfredo. It worked :)

![The Chicken Alfredo in question](img/chicken-alfredo.jpeg)

*It was good. A bit thick - that one's on me, not Otto. It kept telling me to add pasta water and I didn't listen. Lesson learned.*

## What it does

- **Step-by-step guidance.** Walks you through every step with visual cues, temperatures, parallel hints, and timing. Tells you what's coming next so you can prep ahead.
- **Voice output (TTS).** Azure-powered speech so you don't have to stare at your screen with flour on your hands. Audio cached to disk. (Why Azure? I had leftover credits to burn. The TTS interface is swappable, plug in whatever provider you want.)
- **Voice input (STT).** Local Whisper model, no cloud needed. Say "Hey Chef" and start talking.
- **AI recipe modification.** Missing an ingredient? Tell it. It'll adjust, scale, and warn you if the change is going to ruin your dish. Same deal with the GPT backend. Runs on Azure OpenAI right now because free money, but the interface doesn't care where the model lives.
- **Smart timers.** Background timers with escalating notifications. They stay on hold until you say you're ready, and they won't stop yelling until you acknowledge them.
- **Ask questions mid-cook.** The AI has full context of your recipe, current step, and timers. Straight answers, no blog posts.
- **Natural language input.** Type however you want. Keyword parser handles the basics, GPT picks up the rest.
- **Session management.** Pause, resume, skip, check progress. Timers pause with you.
- **Terminal UI.** [Bubble Tea](https://github.com/charmbracelet/bubbletea). Timer bar, color-coded output, clean prompt.

## Getting started

### Prerequisites

- Go 1.24+
- [PortAudio](http://www.portaudio.com/) for audio playback
- Azure Speech key + region (TTS)
- Azure OpenAI / GPT endpoint + key (AI features)
- [whisper.cpp](https://github.com/ggerganov/whisper.cpp) + GGML model (voice input, optional)

### Wake word model files

The wake word detector needs three binary files in `bin/`. Download them before building:

```bash
cd bin/

# ONNX Runtime shared library (macOS ARM64)
curl -L -o ort.tgz https://github.com/microsoft/onnxruntime/releases/download/v1.24.1/onnxruntime-osx-arm64-1.24.1.tgz
tar xzf ort.tgz
cp onnxruntime-osx-arm64-1.24.1/lib/libonnxruntime.dylib .
rm -rf ort.tgz onnxruntime-osx-arm64-1.24.1

# openWakeWord feature models
curl -L -O https://github.com/dscripka/openWakeWord/releases/download/v0.5.1/melspectrogram.onnx
curl -L -O https://github.com/dscripka/openWakeWord/releases/download/v0.5.1/embedding_model.onnx
```

| File | Source | Version |
|------|--------|---------|
| `libonnxruntime.dylib` | [microsoft/onnxruntime](https://github.com/microsoft/onnxruntime/releases/tag/v1.24.1) | v1.24.1 |
| `melspectrogram.onnx` | [dscripka/openWakeWord](https://github.com/dscripka/openWakeWord/releases/tag/v0.5.1) | v0.5.1 |
| `embedding_model.onnx` | [dscripka/openWakeWord](https://github.com/dscripka/openWakeWord/releases/tag/v0.5.1) | v0.5.1 |

> The wakeword model (`hey_otto.onnx`) is included in `models/` and tracked in the repo.

### Build and run

```bash
go build -o bin/ottocook ./cmd/ottocook
./bin/ottocook
```

### Voice input (STT) setup

OttoCook uses [whisper.cpp](https://github.com/ggerganov/whisper.cpp) for local speech-to-text. To enable voice input:

1. Install or build the `whisper-cli` binary from [whisper.cpp](https://github.com/ggerganov/whisper.cpp).
2. Download the GGML model — `ggml-small.bin` works well:
   ```
   https://huggingface.co/ggerganov/whisper.cpp/tree/main
   ```
   Place it in the `bin/` directory (or anywhere you like and point to it with `-whisper-model`).
3. Run with the `-voice` flag:
   ```bash
   ./bin/ottocook -voice
   ```

> **Note:** This has only been tested on macOS (ARM64). The wake word detector depends on the ONNX Runtime dylib and [malgo](https://github.com/gen2brain/malgo) for audio capture, and the Whisper listener uses [portaudio](https://github.com/gordonklaus/portaudio) — both require CGO. Linux should work with the appropriate ONNX Runtime `.so` and PortAudio installed, but it hasn't been tested. Windows is untested and will likely need additional setup (MinGW, MSYS2, etc.). If you don't need voice input, run with `-no-speech` — TTS playback uses [`ebitengine/oto`](https://github.com/ebitengine/oto) which works without CGO.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-verbose` | `false` | Debug logging |
| `-quiet` | `false` | Disable all logging |
| `-no-speech` | `false` | Disable TTS |
| `-no-ai` | `false` | Disable AI agent |
| `-voice` | `false` | Enable voice input via Whisper |
| `-whisper-model` | `bin/ggml-small.bin` | Whisper GGML model path |
| `-disk-cache` | `true` | Persist TTS cache to disk |

## Commands

| Command | What it does |
|---------|-------------|
| `list` | Show available recipes |
| `1`, `2`, `3`... | Select a recipe |
| `start` / `go` | Start cooking |
| `next` / `done` | Next step |
| `skip` | Skip current step |
| `repeat` | Hear current step again |
| `pause` / `resume` | Pause/resume session and timers |
| `status` | Check progress |
| `timer` / `ready` | Start a pending timer |
| `dismiss` / `ok` | Acknowledge a timer |
| `quit` | Exit |

Or just type naturally. *"I only have 2 cloves of garlic"*, *"can I use butter instead?"*, *"double the servings"*. It figures it out.

## Architecture

```
cmd/ottocook/       Entry point + wiring
internal/
  domain/           Core types and interfaces (zero dependencies)
  engine/           Session state machine
  conversation/     Intent parsing + notifications
  gpt/              AI agent (questions, modifications, classification)
  speech/           TTS, STT, audio cache, voice lines
  timer/            Background timer supervisor + session watcher
  display/          Terminal UI (Bubble Tea)
  recipe/           In-memory recipe source
  storage/          In-memory session store
```

Interface-driven, testable, swappable. The domain doesn't care what you plug into it.

Recipes are currently hardcoded in memory, a couple of built-in ones to get started. The plan is to replace that with full recipe generation and persistent storage, but the in-memory source does the job for now and the interface is already there for when that happens.

## Roadmap

Stuff I want to add:

- **Meal planning + shopping lists.** Pick what you want to cook for the week, and it sends the ingredient list to you on Telegram (or whatever) so you have it when you're at the store.
- **Recipe generation + storage.** Instead of static recipes, have the AI generate them based on what you have or what you're in the mood for. Store them properly so they build up over time.
- **Fully local processing.** The end goal. Run the whole thing on a local board with TTS, LLM, everything on-device so I can just have it sitting in my kitchen with zero cloud dependency. The architecture already supports it since everything goes through interfaces. I just don't have the hardware yet.

## License

[MIT](LICENSE). Do whatever you want with it. Just don't burn your dinner.
