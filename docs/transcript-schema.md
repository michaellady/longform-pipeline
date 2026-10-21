# Transcript schema (`analysis/transcript.json`)

Written by `longform prepare` (package `internal/transcribe`). It is the
pipeline's primary transcript (plan D11): the censor step reads word
timestamps from it before upload, and the editorial skill reads it for
chapters and quotes. OpusClip's project transcript is secondary.

```json
{
  "schema_version": "1",
  "source": "whisper.cpp",
  "model": "ggml-medium.en-q5_0.bin",
  "language": "en",
  "duration_ms": 2000,
  "words": [
    { "w": "Hello",  "start_ms": 140,  "end_ms": 380 },
    { "w": "world,", "start_ms": 380,  "end_ms": 940 },
    { "w": "this",   "start_ms": 940,  "end_ms": 1240 },
    { "w": "is",     "start_ms": 1240, "end_ms": 1350 },
    { "w": "a",      "start_ms": 1350, "end_ms": 1430 },
    { "w": "test.",  "start_ms": 1430, "end_ms": 2000 }
  ],
  "segments": [
    { "start_ms": 140, "end_ms": 2000, "text": "Hello world, this is a test." }
  ]
}
```

| Field | Meaning |
|---|---|
| `schema_version` | Always `"1"`. |
| `source` | `"whisper.cpp"`, or `"skipped"` when `LONGFORM_SKIP_TRANSCRIBE=1` or the video has no audio stream (then `words` and `segments` are empty). |
| `model` | Basename of the ggml model file used. Empty when skipped. |
| `language` | Whisper's detected language, else the profile's `transcribe.language` (default `en`). |
| `duration_ms` | Duration of the media that was transcribed (from ffprobe), not of the last word. |
| `words[]` | Every spoken word in order. `w` keeps whisper's casing and trailing punctuation (`"world,"`); match on a cleaned form (lowercase, letters and digits only) as the censor does. `start_ms`/`end_ms` are inclusive-exclusive millisecond bounds in media time. |
| `segments[]` | Sentence-ish groups rebuilt from `words`: a segment ends at a word ending in `.`, `?` or `!`, or when the next word starts more than 1000 ms after the previous one ended. `text` is the words joined by single spaces. |

Arrays are never `null`; an empty transcript has `"words": []` and
`"segments": []`.

## How it is produced

1. `ffmpeg -i <video> -vn -ac 1 -ar 16000 -c:a pcm_s16le analysis/audio16k.wav`
2. `whisper-cli -m <model> -f analysis/audio16k.wav -l en [-t N] -ojf -ml 1 -sow -np -of analysis/whisper`
   writes `analysis/whisper.json`, whisper.cpp's "full" JSON. With
   `--max-len 1 --split-on-word` whisper.cpp 1.9.1 emits one `transcription[]`
   entry per word, each carrying `offsets.from`/`offsets.to` in milliseconds.
   This is the only mode that gives per-word bounds without the DTW
   token-timestamp heuristics (`-dtw`), which need model-specific alignment
   heads and were not reliable on the quantised medium model.
3. `transcribe.Normalize` keeps every entry whose trimmed text is non-empty and
   is not a marker token (`[_BEG_]`, `[_TT_100]`, `[BLANK_AUDIO]`, `(music)`,
   `*laughs*`), splits a multi-word entry by character share if whisper ever
   emits one, and rebuilds segments as described above.

The raw `analysis/whisper.json` is kept beside the transcript for debugging;
`internal/transcribe/testdata/whisper-full.json` is a real capture of it
(macOS `say "hello world this is a test"` through `ggml-small.en.bin`).

## Model resolution

`profile.transcribe.model`, then `LONGFORM_WHISPER_MODEL`, then
`<LONGFORM_CONFIG_DIR>/models/ggml-medium.en-q5_0.bin`
(`~/.config/longform-pipeline/models/...` by default). Models are never
checked into the repo.
