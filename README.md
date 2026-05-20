# Dana API

Dana API is a Go microservice that identifies **movies/TV** or **music** from natural language queries, depending on `MODE`. It returns structured JSON for backend integration.

## Architecture & Features

- **Dual modes:** `MODE=movie` (CineSearch) or `MODE=music` (MusicSearch) — each with its own XML prompt and response schema.
- **Movie pipeline:** LLM identification + TMDB poster enrichment via `imdb_id`.
- **Music pipeline:** LLM identification with album metadata and cover URLs; Google Search grounding when `AI_PROVIDER=google`.
- **Model agnostic:** OpenAI-compatible HTTP (`Authorization: Bearer`, `messages` schema) for Groq, DeepSeek, OpenAI, etc. via `AI_BASE_URL`. Native Google Gemini only when `AI_PROVIDER=google` and `AI_BASE_URL` is empty.
- **Rate limiting:** Configurable global limiter to protect AI API quota.
- **Structured errors:** All API errors return JSON `{"error":"...","details":"..."}` with detailed server logs.
- **Dockerized:** Multi-stage Dockerfile for lightweight deployments.

## Prerequisites

- Go 1.22+
- Docker and Docker Compose (optional)

## Setup and Configuration

1. **Clone the repository:**
   ```bash
   git clone https://github.com/RootedOne/dana-api
   cd dana-api
   ```

2. **Environment Configuration:**
   ```bash
   cp .env.example .env
   ```

   ### Configuration Options

   | Variable | Description | Default |
   |----------|-------------|---------|
   | `AI_PROVIDER` | `google` (native Gemini, no base URL) or `openai` (compatible APIs). | `openai` |
   | `AI_MODEL` | Model identifier. | provider-specific |
   | `AI_API_KEY` | API key (`Authorization: Bearer`). | **Required** |
   | `AI_BASE_URL` | OpenAI-compatible base (e.g. `https://api.groq.com/openai/v1`). **Required for Groq/DeepSeek.** | (empty) |
   | `AI_TEMPERATURE` | Generation temperature. | `0.1` |
   | `MODE` | Service mode: `movie` or `music`. | **Required** |
   | `RATE_LIMIT_ENABLED` | Enable request rate limiting. | `true` |
   | `RATE_LIMIT_REQUESTS` | Max requests per window. | `20` |
   | `RATE_LIMIT_WINDOW_SEC` | Rate limit window in seconds. | `60` |
   | `TMDB_API_KEY` | TMDB key for movie posters (movie mode). | Recommended for movie mode |

   **Movie mode:** `MODE=movie` loads `prompts/system.xml`.

   **Music mode:** `MODE=music` loads `prompts/music.xml`. Uses Google Search when `AI_PROVIDER=google`.

## Running the Service

### Docker Compose (Recommended)

```bash
docker-compose up -d --build
```

### Running Locally

```bash
go mod download
export $(grep -v '^#' .env | xargs) && go run .
```

The service listens on `http://localhost:8080`.

## API Usage

### `POST /sReq`

**Request:**
```json
{
  "query": "A song about a yellow submarine by the Beatles"
}
```

#### Movie mode response (`MODE=movie`)

```json
{
  "guesses": [
    {
      "name": "Yellow Submarine",
      "date": "1969",
      "genre": "Rock",
      "info": "...",
      "imdb_id": "tt0063823",
      "film_cover_art": "https://image.tmdb.org/t/p/w500/..."
    }
  ]
}
```

#### Music mode response (`MODE=music`)

```json
{
  "guesses": [
    {
      "music_name": "Yellow Submarine",
      "music_artist": "The Beatles",
      "album_name": "Yellow Submarine",
      "release_date": "1969-01-17",
      "album_cover": "https://...",
      "info": "..."
    }
  ]
}
```

Both modes return exactly 5 guesses. Non-domain queries return a `SYSTEM ERROR` in the first guess `info` field.

### Error responses

All errors use JSON (not plain text):

```json
{
  "error": "Failed to parse generation response",
  "details": "invalid character 'x' looking for beginning of value"
}
```

| Status | Meaning |
|--------|---------|
| `400` | Invalid JSON or missing query |
| `405` | Method not allowed |
| `429` | Rate limit exceeded |
| `500` | LLM failure, parse error, or internal error |

Server logs include mode, query length, and error details (truncated raw LLM output on parse failures).

## Maintenance & Cleanup

```sh
docker-compose down --rmi all --volumes --remove-orphans && docker builder prune -f
```
