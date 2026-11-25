"""Generates a test season for Loge Server: 3 episodes of ~12 min with a shared
intro and shared end credits, everything else unique per episode. Audio is
sequences of tones in 300-3000 Hz (no silence anywhere), FLAC in MKV, so the
fingerprints of unique parts never match each other.

Usage: python3 -I gen_media.py <media dir> <manifest.json> (e2e/run.sh runs it in the image)
"""
import array
import json
import math
import os
import random
import subprocess
import sys
import wave

RATE = 8000
STEP = 0.25  # seconds per tone chord

# (duration, intro start); intro is 40 s, credits are the last 60 s.
EPISODES = [(700, 20), (720, 35), (740, 50)]
INTRO, CREDITS = 40, 60


def tones(seed, seconds):
    rnd = random.Random(seed)
    n = int(seconds * RATE)
    out = array.array("h", bytes(2 * n))
    per = int(STEP * RATE)
    i = 0
    while i < n:
        f1, f2 = rnd.uniform(300, 3000), rnd.uniform(300, 3000)
        a1, a2 = rnd.uniform(0.3, 0.6), rnd.uniform(0.2, 0.4)
        w1, w2 = 2 * math.pi * f1 / RATE, 2 * math.pi * f2 / RATE
        for k in range(min(per, n - i)):
            out[i + k] = int(12000 * (a1 * math.sin(w1 * k) + a2 * math.sin(w2 * k)))
        i += per
    return out


def main():
    media, manifest = sys.argv[1], sys.argv[2]
    show_dir = os.path.join(media, "Test Show", "Season 01")
    os.makedirs(show_dir, exist_ok=True)
    intro, credits = tones("intro", INTRO), tones("credits", CREDITS)
    items = []
    for n, (dur, at) in enumerate(EPISODES, start=1):
        pcm = array.array("h")
        pcm.extend(tones(f"ep{n}-a", at))
        pcm.extend(intro)
        pcm.extend(tones(f"ep{n}-b", dur - at - INTRO - CREDITS))
        pcm.extend(credits)
        wav = os.path.join(media, f".ep{n}.wav")
        with wave.open(wav, "wb") as w:
            w.setnchannels(1)
            w.setsampwidth(2)
            w.setframerate(RATE)
            w.writeframes(pcm.tobytes())
        name = f"Test Show - S01E0{n}.mkv"
        out = os.path.join(show_dir, name)
        subprocess.run([
            "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
            "-f", "lavfi", "-i", f"testsrc2=s=160x90:r=2:d={dur}",
            "-i", wav,
            "-c:v", "libx264", "-preset", "ultrafast", "-g", "20",
            "-c:a", "flac", "-metadata:s:a:0", "language=rus",
            "-shortest", out,
        ], check=True)
        os.remove(wav)
        probe = subprocess.run([
            "ffprobe", "-v", "error", "-show_entries", "format=duration",
            "-of", "default=nw=1:nk=1", out,
        ], check=True, capture_output=True, text=True)
        items.append({
            "ratingKey": str(100 + n), "partId": 200 + n, "index": n,
            "title": f"Episode {n}",
            "file": "/media/Test Show/Season 01/" + name,
            "size": os.path.getsize(out),
            "duration": int(float(probe.stdout) * 1000),
            "expect": {"intro": [at, at + INTRO], "credits": [dur - CREDITS, dur]},
        })
    with open(manifest, "w") as f:
        json.dump(items, f, indent=1)


if __name__ == "__main__":
    main()
