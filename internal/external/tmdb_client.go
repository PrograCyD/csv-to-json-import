package external

import (
	"encoding/json"
	"fmt"
	"net/http"
	"pc4_etl/internal/models"
	"sync"
	"time"
)

// TMDBClient maneja las peticiones a la API de TMDB con rate limiting y caché
type TMDBClient struct {
	apiKey      string
	httpClient  *http.Client
	rateLimiter <-chan time.Time
	cache       map[string]*models.ExternalData
	cacheMutex  sync.RWMutex
}

// NewTMDBClient crea un nuevo cliente de TMDB con rate limiting
func NewTMDBClient(apiKey string, requestsPerSecond int) *TMDBClient {
	return &TMDBClient{
		apiKey:      apiKey,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		rateLimiter: time.Tick(time.Second / time.Duration(requestsPerSecond)),
		cache:       make(map[string]*models.ExternalData),
	}
}

// FetchMovieData obtiene información de una película desde TMDB
func (c *TMDBClient) FetchMovieData(tmdbID string, title string) (*models.ExternalData, error) {
	// Check cache
	c.cacheMutex.RLock()
	if cached, ok := c.cache[tmdbID]; ok {
		c.cacheMutex.RUnlock()
		return cached, nil
	}
	c.cacheMutex.RUnlock()

	// Rate limiting
	<-c.rateLimiter

	// Fetch movie details
	movieURL := fmt.Sprintf("https://api.themoviedb.org/3/movie/%s?api_key=%s", tmdbID, c.apiKey)
	resp, err := c.httpClient.Get(movieURL)
	if err != nil {
		return nil, fmt.Errorf("error fetching movie details: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		// Movie not found, return empty data
		emptyData := &models.ExternalData{TMDBFetched: false}
		c.cacheMutex.Lock()
		c.cache[tmdbID] = emptyData
		c.cacheMutex.Unlock()
		return emptyData, nil
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("TMDB API returned status %d", resp.StatusCode)
	}

	var movieData models.TMDBMovieResponse
	if err := json.NewDecoder(resp.Body).Decode(&movieData); err != nil {
		return nil, fmt.Errorf("error decoding movie response: %w", err)
	}

	// Fetch credits
	<-c.rateLimiter
	creditsURL := fmt.Sprintf("https://api.themoviedb.org/3/movie/%s/credits?api_key=%s", tmdbID, c.apiKey)
	creditsResp, err := c.httpClient.Get(creditsURL)
	if err != nil {
		return nil, fmt.Errorf("error fetching credits: %w", err)
	}
	defer creditsResp.Body.Close()

	var creditsData models.TMDBCreditsResponse
	if creditsResp.StatusCode == 200 {
		if err := json.NewDecoder(creditsResp.Body).Decode(&creditsData); err != nil {
			// Non-fatal error, continue without credits
			creditsData = models.TMDBCreditsResponse{}
		}
	}

	// Build ExternalData
	externalData := &models.ExternalData{
		Overview:    movieData.Overview,
		Runtime:     movieData.Runtime,
		Budget:      movieData.Budget,
		Revenue:     movieData.Revenue,
		TMDBFetched: true,
	}

	if movieData.PosterPath != "" {
		externalData.PosterURL = fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", movieData.PosterPath)
	}

	// Extract cast (top 10 with profile images)
	maxCast := 10
	for i, member := range creditsData.Cast {
		if i >= maxCast {
			break
		}
		castMember := models.CastMember{Name: member.Name}
		if member.ProfilePath != "" {
			castMember.ProfileURL = fmt.Sprintf("https://image.tmdb.org/t/p/w185%s", member.ProfilePath)
		}
		externalData.Cast = append(externalData.Cast, castMember)
	}

	// Extract director
	for _, member := range creditsData.Crew {
		if member.Job == "Director" {
			externalData.Director = member.Name
			break
		}
	}

	// Cache result
	c.cacheMutex.Lock()
	c.cache[tmdbID] = externalData
	c.cacheMutex.Unlock()

	return externalData, nil
}
