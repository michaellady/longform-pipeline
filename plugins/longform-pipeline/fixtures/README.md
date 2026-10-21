# fixtures

The dry-run input for `/longform-setup` stage 11 and for `make fixture`. **No binary
is committed here**; the file is generated on demand with ffmpeg so the repo stays
small and the fixture is reproducible on any machine.

## sample-30s.mp4

A 30-second, vertical (1080x1920), 30 fps H.264 test pattern with a 440 Hz sine tone,
AAC audio, `faststart`. It is deliberately boring: the pipeline must detect it, hash
it, remux it, transcribe it (whisper emits nothing useful, which is fine), and stage
a job without spending OpusClip credits (`longform stage` stops before upload).

Generate it:

```bash
make fixture
# or, outside the repo:
bash plugins/longform-pipeline/scripts/setup.sh fixture /path/to/sample-30s.mp4
# which runs exactly:
ffmpeg -y -loglevel error \
  -f lavfi -i "testsrc2=size=1080x1920:rate=30:duration=30" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=30" \
  -c:v libx264 -pix_fmt yuv420p -preset veryfast -crf 23 \
  -c:a aac -b:a 96k -shortest -movflags +faststart sample-30s.mp4
```

The wizard writes its copy to `~/.config/longform-pipeline/fixtures/sample-30s.mp4`
and stages it with `longform stage --profile <profile> --file <that path>`.

For a landscape (BJJ-style) fixture swap `size=1080x1920` for `size=1920x1080`. For an
HDR tonemap test, add `-color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc`
to the video encode; `longform prepare` must then produce BT.709 output.

`*.mp4` in this directory should stay untracked; the release tarball ships only the
binary and the wizard generates the fixture at setup time.
