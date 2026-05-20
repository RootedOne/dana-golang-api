package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"golang.org/x/time/rate"
)

const (
	modeMovie = "movie"
	modeMusic = "music"
)

type sReq struct {
	Query string `json:"query"`
}

type Guess struct {
	Name         string `json:"name"`
	Date         string `json:"date"`
	Genre        string `json:"genre"`
	Info         string `json:"info"`
	ImdbID       string `json:"imdb_id"`
	FilmCoverArt string `json:"film_cover_art"`
}

type LLMResponse struct {
	Guesses []Guess `json:"guesses"`
}

type MusicGuess struct {
	MusicName   string `json:"music_name"`
	MusicArtist string `json:"music_artist"`
	AlbumName   string `json:"album_name"`
	ReleaseDate string `json:"release_date"`
	AlbumCover  string `json:"album_cover"`
	Info        string `json:"info"`
}

type MusicLLMResponse struct {
	Guesses []MusicGuess `json:"guesses"`
}

type APIError struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

// TMDB API structures
type TMDBFindResponse struct {
	MovieResults []TMDBResult `json:"movie_results"`
	TvResults    []TMDBResult `json:"tv_results"`
}

type TMDBResult struct {
	PosterPath string `json:"poster_path"`
}

var (
	appMode          string
	aiProvider       string
	systemPrompt     string
	llmClient        llms.Model
	temperature      float64
	tmdbAPIKey       string
	aiAPIKey         string
	aiModel          string
	aiBaseURL        string
	useOpenAICompat  bool
	useNativeGoogle  bool
	rateLimiter      *rate.Limiter
	rateLimitEnabled bool
)

const fallbackCoverArt = "https://via.placeholder.com/500x750.png?text=Cover+Not+Found"
const tmdbImageBaseURL = "https://image.tmdb.org/t/p/w500"

func initEnv() {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MODE")))
	if mode != modeMovie && mode != modeMusic {
		log.Fatal("MODE environment variable is required and must be 'movie' or 'music'")
	}
	appMode = mode

	promptFile := "prompts/system.xml"
	if appMode == modeMusic {
		promptFile = "prompts/music.xml"
	}

	promptData, err := os.ReadFile(promptFile)
	if err != nil {
		log.Fatalf("Failed to read system prompt (%s): %v", promptFile, err)
	}
	systemPrompt = string(promptData)
	log.Printf("MODE=%s prompt=%s", appMode, promptFile)

	aiProvider = strings.ToLower(strings.TrimSpace(os.Getenv("AI_PROVIDER")))
	if aiProvider == "" {
		aiProvider = "google"
	}

	aiModel = os.Getenv("AI_MODEL")
	if aiModel == "" {
		aiModel = "gemini-2.0-flash-lite-preview-02-05"
	}

	aiAPIKey = os.Getenv("AI_API_KEY")
	if aiAPIKey == "" {
		log.Fatal("AI_API_KEY environment variable is required")
	}

	aiBaseURL = strings.TrimSpace(os.Getenv("AI_BASE_URL"))
	useOpenAICompat = aiBaseURL != "" || aiProvider == "openai"
	useNativeGoogle = aiProvider == "google" && aiBaseURL == ""

	if useOpenAICompat {
		if aiBaseURL == "" {
			aiBaseURL = "https://api.openai.com/v1"
		}
		log.Printf("LLM backend: OpenAI-compatible HTTP (%s)", normalizeChatCompletionsURL(aiBaseURL))
	} else if useNativeGoogle {
		ctx := context.Background()
		client, err := googleai.New(
			ctx,
			googleai.WithAPIKey(aiAPIKey),
			googleai.WithDefaultModel(aiModel),
		)
		if err != nil {
			log.Fatalf("Failed to initialize Google AI client: %v", err)
		}
		llmClient = client
		log.Printf("LLM backend: Google Gemini native (model=%s)", aiModel)
		if appMode == modeMusic {
			log.Println("Music mode: Google Search grounding enabled for LLM requests")
		}
	} else {
		log.Fatalf("Unsupported AI_PROVIDER: %s. Use 'google' (no AI_BASE_URL) or 'openai'/Groq with AI_BASE_URL", aiProvider)
	}

	if appMode == modeMovie {
		tmdbAPIKey = os.Getenv("TMDB_API_KEY")
		if tmdbAPIKey == "" {
			log.Println("Warning: TMDB_API_KEY is not set. Fallback posters will be used.")
		}
	}

	tempStr := os.Getenv("AI_TEMPERATURE")
	if tempStr != "" {
		temp, err := strconv.ParseFloat(tempStr, 64)
		if err == nil {
			temperature = temp
		} else {
			temperature = 0.1
		}
	} else {
		temperature = 0.1
	}

	initRateLimiter()
}

func initRateLimiter() {
	enabledStr := strings.ToLower(strings.TrimSpace(os.Getenv("RATE_LIMIT_ENABLED")))
	rateLimitEnabled = enabledStr == "true" || enabledStr == "1"

	if !rateLimitEnabled {
		log.Println("Rate limiting disabled")
		return
	}

	requests := 20
	windowSec := 60

	if v := os.Getenv("RATE_LIMIT_REQUESTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			requests = n
		}
	}
	if v := os.Getenv("RATE_LIMIT_WINDOW_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			windowSec = n
		}
	}

	limit := rate.Limit(float64(requests) / float64(windowSec))
	rateLimiter = rate.NewLimiter(limit, requests)
	log.Printf("Rate limiting enabled: %d requests per %d seconds", requests, windowSec)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func writeJSONError(w http.ResponseWriter, status int, message, details string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := APIError{Error: message, Details: details}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("[%s] failed to encode error response: %v", appMode, err)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[%s] failed to encode response: %v", appMode, err)
	}
}

func cleanJSONResponse(raw string) string {
	cleaned := strings.TrimSpace(raw)
	if strings.HasPrefix(cleaned, "```json") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
	} else if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```")
	}
	if strings.HasSuffix(cleaned, "```") {
		cleaned = strings.TrimSuffix(cleaned, "```")
	}
	return strings.TrimSpace(cleaned)
}

func generateWithNativeGoogle(ctx context.Context, query string) (string, error) {
	content := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, query),
	}

	resp, err := llmClient.GenerateContent(ctx, content, llms.WithTemperature(temperature))
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response choices returned")
	}

	return resp.Choices[0].Content, nil
}

// generateLLM routes to OpenAI-compatible HTTP or native Google based on env.
func generateLLM(ctx context.Context, query string) (string, error) {
	if useOpenAICompat {
		return generateOpenAIChatCompletion(ctx, aiBaseURL, aiAPIKey, aiModel, systemPrompt, query, temperature)
	}

	if useNativeGoogle && appMode == modeMusic {
		return generateMusicWithGoogleSearch(ctx, aiAPIKey, aiModel, systemPrompt, query, temperature)
	}

	if useNativeGoogle {
		return generateWithNativeGoogle(ctx, query)
	}

	return "", fmt.Errorf("no LLM backend configured")
}

func fetchTMDBPoster(imdbID string) string {
	if tmdbAPIKey == "" || imdbID == "" {
		return fallbackCoverArt
	}

	url := fmt.Sprintf("https://api.themoviedb.org/3/find/%s?api_key=%s&external_source=imdb_id", imdbID, tmdbAPIKey)
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return fallbackCoverArt
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackCoverArt
	}

	var tmdbResp TMDBFindResponse
	if err := json.Unmarshal(body, &tmdbResp); err != nil {
		return fallbackCoverArt
	}

	var posterPath string
	if len(tmdbResp.MovieResults) > 0 && tmdbResp.MovieResults[0].PosterPath != "" {
		posterPath = tmdbResp.MovieResults[0].PosterPath
	} else if len(tmdbResp.TvResults) > 0 && tmdbResp.TvResults[0].PosterPath != "" {
		posterPath = tmdbResp.TvResults[0].PosterPath
	}

	if posterPath != "" {
		return tmdbImageBaseURL + posterPath
	}

	return fallbackCoverArt
}

func isValidCoverURL(s string) bool {
	if s == "" || s == "null" {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func normalizeMusicCovers(resp *MusicLLMResponse) {
	for i, guess := range resp.Guesses {
		if strings.HasPrefix(guess.Info, "SYSTEM ERROR") {
			resp.Guesses[i].AlbumCover = fallbackCoverArt
			continue
		}
		if !isValidCoverURL(guess.AlbumCover) {
			resp.Guesses[i].AlbumCover = fallbackCoverArt
		}
	}
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed", "")
		return
	}

	if rateLimitEnabled && rateLimiter != nil && !rateLimiter.Allow() {
		log.Printf("[%s] rate limit exceeded", appMode)
		writeJSONError(w, http.StatusTooManyRequests, "Rate limit exceeded", "Too many requests; try again later")
		return
	}

	var req sReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[%s] invalid JSON payload: %v", appMode, err)
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON payload", err.Error())
		return
	}

	if req.Query == "" {
		log.Printf("[%s] empty query rejected", appMode)
		writeJSONError(w, http.StatusBadRequest, "Query is required", "")
		return
	}

	switch appMode {
	case modeMovie:
		handleMovieSearch(w, r, req)
	case modeMusic:
		handleMusicSearch(w, r, req)
	default:
		writeJSONError(w, http.StatusInternalServerError, "Invalid service mode", appMode)
	}
}

func handleMovieSearch(w http.ResponseWriter, r *http.Request, req sReq) {
	ctx := r.Context()

	resultText, err := generateLLM(ctx, req.Query)
	if err != nil {
		log.Printf("[movie] LLM generation failed query_len=%d err=%v", len(req.Query), err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to generate content", err.Error())
		return
	}

	resultText = cleanJSONResponse(resultText)

	var llmResp LLMResponse
	if err := json.Unmarshal([]byte(resultText), &llmResp); err != nil {
		log.Printf("[movie] JSON parse failed query_len=%d err=%v raw_prefix=%q", len(req.Query), err, truncate(resultText, 200))
		writeJSONError(w, http.StatusInternalServerError, "Failed to parse generation response", err.Error())
		return
	}

	for i, guess := range llmResp.Guesses {
		if guess.ImdbID != "" && guess.ImdbID != "null" && !strings.HasPrefix(guess.Info, "SYSTEM ERROR") {
			llmResp.Guesses[i].FilmCoverArt = fetchTMDBPoster(guess.ImdbID)
		} else {
			llmResp.Guesses[i].FilmCoverArt = fallbackCoverArt
		}
	}

	log.Printf("[movie] success query_len=%d guesses=%d", len(req.Query), len(llmResp.Guesses))
	writeJSON(w, http.StatusOK, llmResp)
}

func handleMusicSearch(w http.ResponseWriter, r *http.Request, req sReq) {
	ctx := r.Context()

	resultText, err := generateLLM(ctx, req.Query)
	if err != nil {
		log.Printf("[music] LLM generation failed query_len=%d err=%v", len(req.Query), err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to generate content", err.Error())
		return
	}

	resultText = cleanJSONResponse(resultText)

	var llmResp MusicLLMResponse
	if err := json.Unmarshal([]byte(resultText), &llmResp); err != nil {
		log.Printf("[music] JSON parse failed query_len=%d err=%v raw_prefix=%q", len(req.Query), err, truncate(resultText, 200))
		writeJSONError(w, http.StatusInternalServerError, "Failed to parse generation response", err.Error())
		return
	}

	normalizeMusicCovers(&llmResp)

	log.Printf("[music] success query_len=%d guesses=%d", len(req.Query), len(llmResp.Guesses))
	writeJSON(w, http.StatusOK, llmResp)
}

func main() {
	initEnv()

	http.HandleFunc("/sReq", handleSearch)

	port := "8080"
	log.Printf("Starting dana-api in %s mode on port %s...", appMode, port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
