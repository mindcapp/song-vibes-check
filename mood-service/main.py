import logging
import os
import re
from typing import Optional

import lyricsgenius
import uvicorn
from dotenv import load_dotenv
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from requests.exceptions import RequestException
from transformers import pipeline

load_dotenv()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("mood-service")

GENIUS_API_KEY = os.getenv("GENIUS_API_KEY", "YOUR_GENIUS_API_KEY_HERE")
SENTIMENT_MODEL = "distilbert-base-uncased-finetuned-sst-2-english"

app = FastAPI(title="mood-service")

genius_client: Optional[lyricsgenius.Genius] = None
sentiment_pipeline = None


class AnalyzeRequest(BaseModel):
    artist: str
    title: str


class AnalyzeResponse(BaseModel):
    mood: str
    confidence: float
    energy: float


@app.on_event("startup")
def load_resources() -> None:
    global genius_client, sentiment_pipeline

    genius_client = lyricsgenius.Genius(
        GENIUS_API_KEY,
        timeout=15,
        retries=2,
        verbose=False,
        remove_section_headers=True,
        skip_non_songs=True,
    )

    logger.info("Loading sentiment model %s ...", SENTIMENT_MODEL)
    sentiment_pipeline = pipeline("sentiment-analysis", model=SENTIMENT_MODEL)
    logger.info("Sentiment model loaded")


@app.get("/health")
def health() -> dict:
    return {"status": "ok"}


def fetch_lyrics(artist: str, title: str) -> str:
    try:
        song = genius_client.search_song(title, artist)
    except RequestException as exc:
        logger.error("Genius API request failed: %s", exc)
        raise HTTPException(status_code=502, detail="Genius API is unavailable") from exc
    except Exception as exc:
        logger.error("Unexpected error while fetching lyrics: %s", exc)
        raise HTTPException(status_code=502, detail="Failed to fetch lyrics") from exc

    if song is None or not song.lyrics:
        raise HTTPException(status_code=404, detail="Track not found")

    return song.lyrics


def clean_lyrics(raw_lyrics: str) -> str:
    text = re.sub(r"^\d*Embed$", "", raw_lyrics, flags=re.MULTILINE)
    text = re.sub(r"\[.*?\]", "", text)
    text = re.sub(r"\s+", " ", text).strip()
    return text


def estimate_energy(text: str) -> float:
    words = text.split()
    if not words:
        return 0.0

    exclamations = text.count("!")
    uppercase_words = sum(1 for w in words if w.isupper() and len(w) > 1)
    unique_ratio = len(set(w.lower() for w in words)) / len(words)

    score = (
        min(exclamations / 10, 1.0) * 0.4
        + min(uppercase_words / 10, 1.0) * 0.3
        + unique_ratio * 0.3
    )
    return round(min(max(score, 0.0), 1.0), 3)


def determine_mood(label: str, energy: float) -> str:
    if label == "POSITIVE":
        return "energetic" if energy >= 0.5 else "happy"
    return "angry" if energy >= 0.5 else "sad"


@app.post("/analyze", response_model=AnalyzeResponse)
def analyze(request: AnalyzeRequest) -> AnalyzeResponse:
    if sentiment_pipeline is None:
        raise HTTPException(status_code=503, detail="Sentiment model is not ready")

    lyrics = fetch_lyrics(request.artist, request.title)
    text = clean_lyrics(lyrics)

    if not text:
        raise HTTPException(status_code=422, detail="No lyrics content to analyze")

    try:
        result = sentiment_pipeline(text[:2000], truncation=True)[0]
    except Exception as exc:
        logger.error("Sentiment analysis failed: %s", exc)
        raise HTTPException(status_code=500, detail="Sentiment analysis failed") from exc

    energy = estimate_energy(text)
    mood = determine_mood(result["label"], energy)

    return AnalyzeResponse(
        mood=mood,
        confidence=round(float(result["score"]), 3),
        energy=energy,
    )


if __name__ == "__main__":
    uvicorn.run("main:app", host="0.0.0.0", port=8001)
