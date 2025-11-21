package main

import (
	"bufio"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var yearRe = regexp.MustCompile(`\((\d{4})\)\s*$`)

// loadEnvFile carga variables de entorno desde un archivo .env
func loadEnvFile(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Ignorar líneas vacías y comentarios
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Parsear KEY=VALUE
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			// Remover comillas si existen
			value = strings.Trim(value, "\"'")
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func isoNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func parseTitleAndYear(raw string) (string, *int) {
	raw = strings.TrimSpace(raw)
	m := yearRe.FindStringSubmatch(raw)
	if len(m) == 2 {
		y, err := strconv.Atoi(m[1])
		if err == nil {
			// remove last occurrence of (YYYY)
			idx := strings.LastIndex(raw, "(")
			if idx > 0 {
				title := strings.TrimSpace(raw[:idx])
				return title, &y
			}
			return strings.TrimSpace(raw), &y
		}
	}
	// fallback: no year
	return raw, nil
}

type Links struct {
	Movielens string `json:"movielens,omitempty"`
	IMDB      string `json:"imdb,omitempty"`
	TMDB      string `json:"tmdb,omitempty"`
}

type GenomeTag struct {
	Tag       string  `json:"tag"`
	Relevance float64 `json:"relevance"`
}

type RatingStats struct {
	Average     float64 `json:"average"`
	Count       int     `json:"count"`
	LastRatedAt string  `json:"lastRatedAt,omitempty"`
}

type CastMember struct {
	Name       string `json:"name"`
	ProfileURL string `json:"profileUrl,omitempty"`
}

type ExternalData struct {
	PosterURL   string       `json:"posterUrl,omitempty"`
	Overview    string       `json:"overview,omitempty"`
	Cast        []CastMember `json:"cast,omitempty"`
	Director    string       `json:"director,omitempty"`
	Runtime     int          `json:"runtime,omitempty"`
	Budget      int          `json:"budget,omitempty"`
	Revenue     int64        `json:"revenue,omitempty"`
	TMDBFetched bool         `json:"tmdbFetched"`
}

type MovieDoc struct {
	MovieID      int           `json:"movieId"`
	IIdx         *int          `json:"iIdx,omitempty"`
	Title        string        `json:"title"`
	Year         *int          `json:"year,omitempty"`
	Genres       []string      `json:"genres"`
	Links        *Links        `json:"links,omitempty"`
	GenomeTags   []GenomeTag   `json:"genomeTags,omitempty"`
	UserTags     []string      `json:"userTags,omitempty"`
	RatingStats  *RatingStats  `json:"ratingStats,omitempty"`
	ExternalData *ExternalData `json:"externalData,omitempty"`
	CreatedAt    string        `json:"createdAt"`
	UpdatedAt    string        `json:"updatedAt"`
}

type RatingDoc struct {
	UserID    int     `json:"userId"`
	MovieID   int     `json:"movieId"`
	Rating    float64 `json:"rating"`
	Timestamp int64   `json:"timestamp"`
}

type UserDoc struct {
	UserID       int    `json:"userId"`
	UIdx         *int   `json:"uIdx,omitempty"`
	Email        string `json:"email"`
	PasswordHash string `json:"passwordHash"`
	Role         string `json:"role"`
	CreatedAt    string `json:"createdAt"`
}

type Neighbor struct {
	MovieID int     `json:"movieId"`
	IIdx    int     `json:"iIdx"`
	Sim     float64 `json:"sim"`
}

type SimilarityDoc struct {
	ID        string     `json:"_id"`
	MovieID   int        `json:"movieId"`
	IIdx      int        `json:"iIdx"`
	Metric    string     `json:"metric"`
	K         int        `json:"k"`
	Neighbors []Neighbor `json:"neighbors"`
	UpdatedAt string     `json:"updatedAt"`
}

// Estructuras para respuestas de TMDB API
type TMDBMovieResponse struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Overview    string `json:"overview"`
	PosterPath  string `json:"poster_path"`
	Runtime     int    `json:"runtime"`
	Budget      int    `json:"budget"`
	Revenue     int64  `json:"revenue"`
	ReleaseDate string `json:"release_date"`
}

type TMDBCreditsResponse struct {
	Cast []struct {
		Name        string `json:"name"`
		Character   string `json:"character"`
		Order       int    `json:"order"`
		ProfilePath string `json:"profile_path"`
	} `json:"cast"`
	Crew []struct {
		Name string `json:"name"`
		Job  string `json:"job"`
	} `json:"crew"`
}

// Cliente TMDB con rate limiting y cache
type TMDBClient struct {
	apiKey      string
	httpClient  *http.Client
	rateLimiter <-chan time.Time
	cache       map[string]*ExternalData
	cacheMutex  sync.RWMutex
}

func NewTMDBClient(apiKey string, requestsPerSecond int) *TMDBClient {
	return &TMDBClient{
		apiKey:      apiKey,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		rateLimiter: time.Tick(time.Second / time.Duration(requestsPerSecond)),
		cache:       make(map[string]*ExternalData),
	}
}

func (c *TMDBClient) FetchMovieData(tmdbID string, title string) (*ExternalData, error) {
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
		emptyData := &ExternalData{TMDBFetched: false}
		c.cacheMutex.Lock()
		c.cache[tmdbID] = emptyData
		c.cacheMutex.Unlock()
		return emptyData, nil
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("TMDB API returned status %d", resp.StatusCode)
	}

	var movieData TMDBMovieResponse
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

	var creditsData TMDBCreditsResponse
	if creditsResp.StatusCode == 200 {
		if err := json.NewDecoder(creditsResp.Body).Decode(&creditsData); err != nil {
			// Non-fatal error, continue without credits
			creditsData = TMDBCreditsResponse{}
		}
	}

	// Build ExternalData
	externalData := &ExternalData{
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
		castMember := CastMember{Name: member.Name}
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

// loadLinks carga los links desde links.csv
func loadLinks(path string) (map[int]*Links, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	links := make(map[int]*Links)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 3 {
			continue
		}

		movieId, _ := strconv.Atoi(rec[0])
		imdbId := strings.TrimSpace(rec[1])
		tmdbId := strings.TrimSpace(rec[2])

		link := &Links{}
		if movieId > 0 {
			link.Movielens = fmt.Sprintf("https://movielens.org/movies/%d", movieId)
		}
		if imdbId != "" {
			link.IMDB = fmt.Sprintf("http://www.imdb.com/title/tt%s/", imdbId)
		}
		if tmdbId != "" {
			link.TMDB = fmt.Sprintf("https://www.themoviedb.org/movie/%s", tmdbId)
		}

		links[movieId] = link
	}
	return links, nil
}

// loadGenomeTags carga el mapeo de tagId -> tag
func loadGenomeTags(path string) (map[int]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	tags := make(map[int]string)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 2 {
			continue
		}

		tagId, _ := strconv.Atoi(rec[0])
		tag := strings.TrimSpace(rec[1])
		tags[tagId] = tag
	}
	return tags, nil
}

// loadGenomeScores carga los scores de relevancia (movieId -> tagId -> relevance)
func loadGenomeScores(path string, genomeTagsMap map[int]string, minRelevance float64) (map[int][]GenomeTag, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	scores := make(map[int][]GenomeTag)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 3 {
			continue
		}

		movieId, _ := strconv.Atoi(rec[0])
		tagId, _ := strconv.Atoi(rec[1])
		relevance, _ := strconv.ParseFloat(rec[2], 64)

		// Filtrar solo tags con relevancia mayor al umbral
		if relevance >= minRelevance {
			if tagName, ok := genomeTagsMap[tagId]; ok {
				scores[movieId] = append(scores[movieId], GenomeTag{
					Tag:       tagName,
					Relevance: relevance,
				})
			}
		}
	}

	// Ordenar por relevancia descendente
	for movieId := range scores {
		sort.Slice(scores[movieId], func(i, j int) bool {
			return scores[movieId][i].Relevance > scores[movieId][j].Relevance
		})
	}

	return scores, nil
}

// normalizeTag normaliza un tag para eliminar duplicados y typos
func normalizeTag(tag string) string {
	// Convertir a minúsculas y trim
	tag = strings.TrimSpace(strings.ToLower(tag))

	// Eliminar múltiples espacios consecutivos
	tag = regexp.MustCompile(`\s+`).ReplaceAllString(tag, " ")

	// Eliminar caracteres especiales al inicio/final
	tag = strings.Trim(tag, ".,;:!?\"'`-_")

	return tag
}

// UserTagWithFrequency estructura para ordenar tags por frecuencia
type UserTagWithFrequency struct {
	Tag       string
	Frequency int
}

// loadUserTags carga los tags de usuarios con frecuencia (movieId -> []tag ordenados por popularidad)
func loadUserTags(path string) (map[int][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	// Estructura: movieId -> tag normalizado -> set de userIds que lo asignaron
	tagFrequency := make(map[int]map[string]map[int]struct{})

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 4 {
			continue
		}

		userId, _ := strconv.Atoi(rec[0])
		movieId, _ := strconv.Atoi(rec[1])
		tag := normalizeTag(rec[2])

		if tag != "" && movieId > 0 && userId > 0 {
			if tagFrequency[movieId] == nil {
				tagFrequency[movieId] = make(map[string]map[int]struct{})
			}
			if tagFrequency[movieId][tag] == nil {
				tagFrequency[movieId][tag] = make(map[int]struct{})
			}
			// Agregar el userId al set (para contar usuarios únicos)
			tagFrequency[movieId][tag][userId] = struct{}{}
		}
	}

	// Convertir a lista ordenada por frecuencia (top 10)
	result := make(map[int][]string)
	for movieId, tags := range tagFrequency {
		// Crear lista de tags con su frecuencia
		tagList := make([]UserTagWithFrequency, 0, len(tags))
		for tag, users := range tags {
			tagList = append(tagList, UserTagWithFrequency{
				Tag:       tag,
				Frequency: len(users), // Número de usuarios únicos que asignaron este tag
			})
		}

		// Ordenar por frecuencia descendente, luego alfabéticamente
		sort.Slice(tagList, func(i, j int) bool {
			if tagList[i].Frequency != tagList[j].Frequency {
				return tagList[i].Frequency > tagList[j].Frequency
			}
			return tagList[i].Tag < tagList[j].Tag
		})

		// Tomar top 10 más frecuentes
		maxTags := 10
		if len(tagList) > maxTags {
			tagList = tagList[:maxTags]
		}

		// Extraer solo los nombres de los tags
		finalTags := make([]string, len(tagList))
		for i, t := range tagList {
			finalTags[i] = t.Tag
		}

		result[movieId] = finalTags
	}

	return result, nil
}

// loadRatingStats calcula estadísticas de ratings (movieId -> stats)
func loadRatingStats(path string) (map[int]*RatingStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	// Acumuladores
	type accumulator struct {
		sum    float64
		count  int
		lastTs int64
	}

	accums := make(map[int]*accumulator)

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 4 {
			continue
		}

		movieId, _ := strconv.Atoi(rec[1])
		rating, _ := strconv.ParseFloat(rec[2], 64)
		timestamp, _ := strconv.ParseInt(rec[3], 10, 64)

		if accums[movieId] == nil {
			accums[movieId] = &accumulator{}
		}

		accums[movieId].sum += rating
		accums[movieId].count++
		if timestamp > accums[movieId].lastTs {
			accums[movieId].lastTs = timestamp
		}
	}

	// Calcular promedios
	stats := make(map[int]*RatingStats)
	for movieId, acc := range accums {
		if acc.count > 0 {
			avg := acc.sum / float64(acc.count)
			lastRatedAt := ""
			if acc.lastTs > 0 {
				lastRatedAt = time.Unix(acc.lastTs, 0).UTC().Format(time.RFC3339)
			}
			stats[movieId] = &RatingStats{
				Average:     avg,
				Count:       acc.count,
				LastRatedAt: lastRatedAt,
			}
		}
	}

	return stats, nil
}

// loadItemMap carga el mapeo movieId -> iIdx desde item_map.csv
func loadItemMap(path string) (map[int]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	itemMap := make(map[int]int)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 2 {
			continue
		}

		movieId, _ := strconv.Atoi(rec[0])
		iIdx, _ := strconv.Atoi(rec[1])

		if movieId > 0 {
			itemMap[movieId] = iIdx
		}
	}

	return itemMap, nil
}

// loadUserMap carga el mapeo userId -> uIdx desde user_map.csv
func loadUserMap(path string) (map[int]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	userMap := make(map[int]int)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 2 {
			continue
		}

		userId, _ := strconv.Atoi(rec[0])
		uIdx, _ := strconv.Atoi(rec[1])

		if userId > 0 {
			userMap[userId] = uIdx
		}
	}

	return userMap, nil
}

// generateRandomPassword genera un password de 10 dígitos aleatorios
func generateRandomPassword() (string, error) {
	password := ""
	for i := 0; i < 10; i++ {
		digit, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		password += digit.String()
	}
	return password, nil
}

// hashPassword hashea un password usando bcrypt
func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// processUsers genera users.ndjson con passwords hasheados
func processUsers(ratingsPath, outPath, passwordLogPath string, userMap map[int]int, hashPasswords bool) (int, error) {
	// Primero, leer ratings para obtener todos los usuarios únicos
	f, err := os.Open(ratingsPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return 0, err
	}

	users := make(map[int]struct{})
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 1 {
			continue
		}
		uid, _ := strconv.Atoi(rec[0])
		if uid > 0 {
			users[uid] = struct{}{}
		}
	}

	// Ordenar userIds
	userIds := make([]int, 0, len(users))
	for uid := range users {
		userIds = append(userIds, uid)
	}
	sort.Ints(userIds)

	// Crear archivo de salida
	of, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer of.Close()
	w := bufio.NewWriter(of)
	defer w.Flush()

	// Crear log de passwords
	logFile, err := os.Create(passwordLogPath)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	logWriter := bufio.NewWriter(logFile)
	defer logWriter.Flush()

	// Header del log
	logWriter.WriteString("userId,uIdx,email,password,passwordHash\n")

	now := isoNow()
	written := 0

	for _, uid := range userIds {
		// Generar email
		email := fmt.Sprintf("user%d@email.com", uid)

		// Generar password aleatorio de 10 dígitos
		password, err := generateRandomPassword()
		if err != nil {
			continue
		}

		// Hashear password solo si está habilitado
		passwordHash := password
		if hashPasswords {
			hashed, err := hashPassword(password)
			if err != nil {
				continue
			}
			passwordHash = hashed
		}

		// Crear documento
		doc := UserDoc{
			UserID:       uid,
			Email:        email,
			PasswordHash: passwordHash,
			Role:         "user",
			CreatedAt:    now,
		}

		// Agregar uIdx si existe
		if uIdx, ok := userMap[uid]; ok {
			doc.UIdx = &uIdx
		}

		// Escribir NDJSON
		b, _ := json.Marshal(doc)
		w.Write(b)
		w.WriteByte('\n')

		// Escribir log
		uIdxStr := "null"
		if doc.UIdx != nil {
			uIdxStr = fmt.Sprintf("%d", *doc.UIdx)
		}
		logWriter.WriteString(fmt.Sprintf("%d,%s,%s,%s,%s\n", uid, uIdxStr, email, password, passwordHash))

		written++
	}

	return written, nil
}

// loadSimilarities carga las similitudes desde item_topk_cosine_conc.csv
func loadSimilarities(path string, itemMap map[int]int) (map[int][]Neighbor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// skip header
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	// Crear reverse map: iIdx -> movieId
	reverseMap := make(map[int]int)
	for movieId, iIdx := range itemMap {
		reverseMap[iIdx] = movieId
	}

	// Agrupar por iIdx
	similarities := make(map[int][]Neighbor)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 3 {
			continue
		}

		iIdx, _ := strconv.Atoi(rec[0])
		jIdx, _ := strconv.Atoi(rec[1])
		sim, _ := strconv.ParseFloat(rec[2], 64)

		if iIdx > 0 && jIdx > 0 {
			// Obtener movieId del jIdx
			jMovieId := reverseMap[jIdx]

			neighbor := Neighbor{
				MovieID: jMovieId,
				IIdx:    jIdx,
				Sim:     sim,
			}
			similarities[iIdx] = append(similarities[iIdx], neighbor)
		}
	}

	return similarities, nil
}

// processSimilarities genera similarities.ndjson
func processSimilarities(outPath string, similarities map[int][]Neighbor, itemMap map[int]int) (int, error) {
	// Crear reverse map: iIdx -> movieId
	reverseMap := make(map[int]int)
	for movieId, iIdx := range itemMap {
		reverseMap[iIdx] = movieId
	}

	// Crear archivo de salida
	of, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer of.Close()
	w := bufio.NewWriter(of)
	defer w.Flush()

	now := isoNow()
	written := 0

	for iIdx, neighbors := range similarities {
		movieId := reverseMap[iIdx]

		// Limitar a k=20
		if len(neighbors) > 20 {
			neighbors = neighbors[:20]
		}

		doc := SimilarityDoc{
			ID:        fmt.Sprintf("%d_cosine_k20", iIdx),
			MovieID:   movieId,
			IIdx:      iIdx,
			Metric:    "cosine",
			K:         len(neighbors),
			Neighbors: neighbors,
			UpdatedAt: now,
		}

		b, _ := json.Marshal(doc)
		w.Write(b)
		w.WriteByte('\n')
		written++
	}

	return written, nil
}

func processMovies(inPath, outPath string, links map[int]*Links, genomeTags map[int][]GenomeTag, userTags map[int][]string, ratingStats map[int]*RatingStats, itemMap map[int]int, topGenomeTags int, tmdbClient *TMDBClient, fetchExternal bool) (int, error) {
	f, err := os.Open(inPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	// open output
	of, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer of.Close()
	w := bufio.NewWriter(of)
	defer w.Flush()

	// read header
	header, err := r.Read()
	if err != nil {
		return 0, err
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[h] = i
	}

	written := 0
	now := isoNow()
	fetchedCount := 0
	errorCount := 0

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// skip malformed
			continue
		}
		// guard indexes
		mid := 0
		if v, ok := idx["movieId"]; ok && v < len(rec) {
			mid, _ = strconv.Atoi(rec[v])
		} else if len(rec) > 0 {
			mid, _ = strconv.Atoi(rec[0])
		}
		titleRaw := ""
		if v, ok := idx["title"]; ok && v < len(rec) {
			titleRaw = rec[v]
		} else if len(rec) > 1 {
			titleRaw = rec[1]
		}
		genresRaw := ""
		if v, ok := idx["genres"]; ok && v < len(rec) {
			genresRaw = rec[v]
		} else if len(rec) > 2 {
			genresRaw = rec[2]
		}

		title, year := parseTitleAndYear(titleRaw)
		genres := []string{}
		if genresRaw != "" && genresRaw != "(no genres listed)" {
			for _, g := range strings.Split(genresRaw, "|") {
				g = strings.TrimSpace(g)
				if g != "" {
					genres = append(genres, g)
				}
			}
		}

		doc := MovieDoc{
			MovieID:   mid,
			Title:     title,
			Year:      year,
			Genres:    genres,
			CreatedAt: now,
			UpdatedAt: now,
		}

		// Agregar iIdx si existe en el mapeo
		if iIdx, ok := itemMap[mid]; ok {
			doc.IIdx = &iIdx
		}

		// Agregar links si existen
		if link, ok := links[mid]; ok {
			doc.Links = link
		}

		// Agregar genome tags (limitado a top N más relevantes)
		if gTags, ok := genomeTags[mid]; ok {
			if len(gTags) > topGenomeTags {
				doc.GenomeTags = gTags[:topGenomeTags]
			} else {
				doc.GenomeTags = gTags
			}
		}

		// Agregar user tags (ya limitados a top 10 por frecuencia en loadUserTags)
		if uTags, ok := userTags[mid]; ok {
			doc.UserTags = uTags
		}

		// Agregar rating stats
		if stats, ok := ratingStats[mid]; ok {
			doc.RatingStats = stats
		}

		// Fetch external data from TMDB if enabled
		if fetchExternal && tmdbClient != nil && doc.Links != nil && doc.Links.TMDB != "" {
			// Extract TMDB ID from URL
			tmdbURL := doc.Links.TMDB
			parts := strings.Split(tmdbURL, "/")
			if len(parts) > 0 {
				tmdbID := parts[len(parts)-1]
				if tmdbID != "" {
					externalData, err := tmdbClient.FetchMovieData(tmdbID, title)
					if err != nil {
						errorCount++
						if errorCount%100 == 0 {
							fmt.Fprintf(os.Stderr, "  ⚠ %d errores al consultar TMDB...\n", errorCount)
						}
					} else if externalData != nil && externalData.TMDBFetched {
						doc.ExternalData = externalData
						fetchedCount++
						if fetchedCount%100 == 0 {
							fmt.Printf("  ℹ %d películas enriquecidas con TMDB...\n", fetchedCount)
						}
					}
				}
			}
		}

		b, _ := json.Marshal(doc)
		w.Write(b)
		w.WriteByte('\n')
		written++
	}

	if fetchExternal {
		fmt.Printf("  ✓ %d películas enriquecidas con datos de TMDB\n", fetchedCount)
		if errorCount > 0 {
			fmt.Printf("  ⚠ %d errores al consultar TMDB\n", errorCount)
		}
	}

	return written, nil
}

func processRatings(inPath, outPath string) (int, error) {
	f, err := os.Open(inPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1

	of, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer of.Close()
	w := bufio.NewWriter(of)
	defer w.Flush()

	header, err := r.Read()
	if err != nil {
		return 0, err
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[h] = i
	}

	written := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		uid := 0
		mid := 0
		rating := 0.0
		ts := int64(0)
		if v, ok := idx["userId"]; ok && v < len(rec) {
			uid, _ = strconv.Atoi(rec[v])
		} else if len(rec) > 0 {
			uid, _ = strconv.Atoi(rec[0])
		}
		if v, ok := idx["movieId"]; ok && v < len(rec) {
			mid, _ = strconv.Atoi(rec[v])
		} else if len(rec) > 1 {
			mid, _ = strconv.Atoi(rec[1])
		}
		if v, ok := idx["rating"]; ok && v < len(rec) {
			rating, _ = strconv.ParseFloat(rec[v], 64)
		} else if len(rec) > 2 {
			rating, _ = strconv.ParseFloat(rec[2], 64)
		}
		if v, ok := idx["timestamp"]; ok && v < len(rec) {
			ts, _ = strconv.ParseInt(rec[v], 10, 64)
		} else if len(rec) > 3 {
			ts, _ = strconv.ParseInt(rec[3], 10, 64)
		}

		doc := RatingDoc{
			UserID:    uid,
			MovieID:   mid,
			Rating:    rating,
			Timestamp: ts,
		}
		b, _ := json.Marshal(doc)
		w.Write(b)
		w.WriteByte('\n')
		written++
	}

	return written, nil
}

func main() {
	// Intentar cargar .env antes de parsear flags
	if err := loadEnvFile(".env"); err == nil {
		fmt.Println("✓ Archivo .env cargado")
	}

	dataDir := flag.String("data-dir", "data", "Directorio con los csv (default: data)")
	moviesFile := flag.String("movies-file", "movies.csv", "Nombre de movies.csv")
	ratingsFile := flag.String("ratings-file", "ratings.csv", "Nombre de ratings.csv")
	linksFile := flag.String("links-file", "links.csv", "Nombre de links.csv")
	tagsFile := flag.String("tags-file", "tags.csv", "Nombre de tags.csv")
	genomeTagsFile := flag.String("genome-tags-file", "genome-tags.csv", "Nombre de genome-tags.csv")
	genomeScoresFile := flag.String("genome-scores-file", "genome-scores.csv", "Nombre de genome-scores.csv")
	itemMapFile := flag.String("item-map-file", "item_map.csv", "Nombre de item_map.csv")
	userMapFile := flag.String("user-map-file", "user_map.csv", "Nombre de user_map.csv")
	similaritiesFile := flag.String("similarities-file", "item_topk_cosine_conc.csv", "Nombre de item_topk_cosine_conc.csv")
	outDir := flag.String("out-dir", "out", "Directorio de salida para NDJSON")
	minRelevance := flag.Float64("min-relevance", 0.5, "Relevancia mínima para genome tags (0.0-1.0)")
	topGenomeTags := flag.Int("top-genome-tags", 10, "Número máximo de genome tags por película")
	hashPasswords := flag.Bool("hash-passwords", true, "Hashear passwords con bcrypt (más lento pero seguro)")

	// TMDB API flags
	tmdbAPIKey := flag.String("tmdb-api-key", "", "TMDB API Key (opcional, se lee de .env si no se especifica)")
	fetchExternal := flag.Bool("fetch-external", false, "Fetch datos externos desde TMDB API")
	tmdbRateLimit := flag.Int("tmdb-rate-limit", 4, "Requests por segundo a TMDB API (default: 4)")

	flag.Parse()

	// Si no se especificó API key por flag, intentar leerla de variable de entorno
	if *tmdbAPIKey == "" {
		*tmdbAPIKey = os.Getenv("TMDB_API_KEY")
	}

	os.MkdirAll(*outDir, 0o755)

	// Rutas de archivos
	moviesPath := filepath.Join(*dataDir, *moviesFile)
	ratingsPath := filepath.Join(*dataDir, *ratingsFile)
	linksPath := filepath.Join(*dataDir, *linksFile)
	tagsPath := filepath.Join(*dataDir, *tagsFile)
	genomeTagsPath := filepath.Join(*dataDir, *genomeTagsFile)
	genomeScoresPath := filepath.Join(*dataDir, *genomeScoresFile)
	itemMapPath := filepath.Join(*dataDir, *itemMapFile)
	userMapPath := filepath.Join(*dataDir, *userMapFile)
	similaritiesPath := filepath.Join(*dataDir, *similaritiesFile)

	moviesOut := filepath.Join(*outDir, "movies.ndjson")
	ratingsOut := filepath.Join(*outDir, "ratings.ndjson")
	usersOut := filepath.Join(*outDir, "users.ndjson")
	similaritiesOut := filepath.Join(*outDir, "similarities.ndjson")
	passwordLogOut := filepath.Join(*outDir, "passwords_log.csv")

	// Determinar fase del ETL
	phase := "Fase 1"
	if *fetchExternal && *tmdbAPIKey != "" {
		phase = "Fase 2 (con datos externos de TMDB)"
	}

	fmt.Printf("=== ETL para MongoDB - %s ===\n", phase)
	fmt.Println()

	// Inicializar cliente TMDB si es necesario
	var tmdbClient *TMDBClient
	if *fetchExternal {
		if *tmdbAPIKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --fetch-external requiere --tmdb-api-key")
			fmt.Fprintln(os.Stderr, "Obtén tu API key en: https://www.themoviedb.org/settings/api")
			os.Exit(1)
		}
		tmdbClient = NewTMDBClient(*tmdbAPIKey, *tmdbRateLimit)
		fmt.Printf("✓ Cliente TMDB inicializado (rate limit: %d req/s)\n", *tmdbRateLimit)
		fmt.Println()
	}

	// Cargar datos complementarios
	fmt.Println("Cargando links...")
	links, err := loadLinks(linksPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar links.csv: %v\n", err)
		links = make(map[int]*Links)
	}
	fmt.Printf("  ✓ %d links cargados\n", len(links))

	fmt.Println("Cargando genome tags...")
	genomeTagsMap, err := loadGenomeTags(genomeTagsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar genome-tags.csv: %v\n", err)
		genomeTagsMap = make(map[int]string)
	}
	fmt.Printf("  ✓ %d genome tags cargados\n", len(genomeTagsMap))

	fmt.Println("Cargando genome scores...")
	genomeScores, err := loadGenomeScores(genomeScoresPath, genomeTagsMap, *minRelevance)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar genome-scores.csv: %v\n", err)
		genomeScores = make(map[int][]GenomeTag)
	}
	fmt.Printf("  ✓ Genome scores cargados para %d películas (relevancia >= %.2f)\n", len(genomeScores), *minRelevance)

	fmt.Println("Cargando user tags...")
	userTags, err := loadUserTags(tagsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar tags.csv: %v\n", err)
		userTags = make(map[int][]string)
	}
	fmt.Printf("  ✓ User tags cargados para %d películas\n", len(userTags))

	fmt.Println("Calculando estadísticas de ratings...")
	ratingStats, err := loadRatingStats(ratingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar ratings.csv: %v\n", err)
		ratingStats = make(map[int]*RatingStats)
	}
	fmt.Printf("  ✓ Estadísticas calculadas para %d películas\n", len(ratingStats))

	fmt.Println("Cargando mapeo de items...")
	itemMap, err := loadItemMap(itemMapPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar item_map.csv: %v\n", err)
		itemMap = make(map[int]int)
	}
	fmt.Printf("  ✓ Mapeo de items cargado para %d películas\n", len(itemMap))

	fmt.Println("Cargando mapeo de usuarios...")
	userMap, err := loadUserMap(userMapPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar user_map.csv: %v\n", err)
		userMap = make(map[int]int)
	}
	fmt.Printf("  ✓ Mapeo de usuarios cargado para %d usuarios\n", len(userMap))

	fmt.Println()
	if *fetchExternal {
		fmt.Println("Procesando movies con datos externos de TMDB:", moviesPath)
		fmt.Println("  ⏳ Esto puede tardar varios minutos debido al rate limiting...")
	} else {
		fmt.Println("Procesando movies:", moviesPath)
	}
	mcount, merr := processMovies(moviesPath, moviesOut, links, genomeScores, userTags, ratingStats, itemMap, *topGenomeTags, tmdbClient, *fetchExternal)
	if merr != nil {
		fmt.Fprintln(os.Stderr, "error procesando movies:", merr)
		os.Exit(1)
	}
	fmt.Printf("  ✓ Escritas %d películas en %s\n", mcount, moviesOut)

	fmt.Println()
	fmt.Println("Procesando ratings:", ratingsPath)
	rcount, rerr := processRatings(ratingsPath, ratingsOut)
	if rerr != nil {
		fmt.Fprintln(os.Stderr, "error procesando ratings:", rerr)
		os.Exit(1)
	}
	fmt.Printf("  ✓ Escritas %d entradas en %s\n", rcount, ratingsOut)

	fmt.Println()
	fmt.Println("Generando users con passwords hasheados...")
	ucount, uerr := processUsers(ratingsPath, usersOut, passwordLogOut, userMap, *hashPasswords)
	if uerr != nil {
		fmt.Fprintln(os.Stderr, "error generando users:", uerr)
		os.Exit(1)
	}
	fmt.Printf("  ✓ Generados %d usuarios en %s\n", ucount, usersOut)
	if *hashPasswords {
		fmt.Printf("  ✓ Passwords hasheados con bcrypt\n")
	} else {
		fmt.Printf("  ⚠ Passwords sin hashear (modo rápido)\n")
	}
	fmt.Printf("  ✓ Log de passwords guardado en %s\n", passwordLogOut)

	fmt.Println()
	fmt.Println("Cargando similitudes desde", similaritiesPath, "...")
	similarities, serr := loadSimilarities(similaritiesPath, itemMap)
	if serr != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar similitudes: %v\n", serr)
		similarities = make(map[int][]Neighbor)
	}
	fmt.Printf("  ✓ Similitudes cargadas para %d películas\n", len(similarities))

	fmt.Println("Generando similarities...")
	scount, serr2 := processSimilarities(similaritiesOut, similarities, itemMap)
	if serr2 != nil {
		fmt.Fprintln(os.Stderr, "error generando similarities:", serr2)
		os.Exit(1)
	}
	fmt.Printf("  ✓ Generadas %d entradas de similitud en %s\n", scount, similaritiesOut)

	fmt.Println()
	fmt.Println("=== ETL completado exitosamente ===")
}
