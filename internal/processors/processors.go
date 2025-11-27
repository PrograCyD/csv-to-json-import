package processors

import (
	"bufio"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	mathrand "math/rand"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pc4_etl/internal/external"
	"pc4_etl/internal/mappers"
	"pc4_etl/internal/models"
	"pc4_etl/internal/utils"

	"github.com/jaswdr/faker"
	"golang.org/x/crypto/bcrypt"
)

// isoNow retorna la fecha actual en formato ISO 8601
func isoNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// parseTitleAndYear extrae el título y año de una cadena como "Movie Title (2020)"
func parseTitleAndYear(raw string, yearRe *regexp.Regexp) (string, *int) {
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

// ProcessUsers genera users.ndjson con passwords hasheados
func ProcessUsers(ratingsPath, outPath, passwordLogPath string, userMapper *mappers.IDMapper, hashPasswords bool, allGenres []string) (int, error) {
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

	// Header del log (actualizado con nuevos campos)
	logWriter.WriteString("userId,uIdx,firstName,lastName,username,email,password,passwordHash\n")

	// Inicializar faker y random
	fake := faker.New()
	mathrand.Seed(time.Now().UnixNano())

	now := isoNow()
	written := 0

	for _, uid := range userIds {
		// Generar nombre y apellido con faker
		firstName, lastName := utils.GenerateRandomName(fake)

		// Generar username
		username := utils.GenerateUsername(firstName, lastName, uid)

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

		// Seleccionar géneros favoritos aleatorios
		preferredGenres := utils.SelectRandomGenres(allGenres)

		// Generar About basado en los géneros
		about := utils.GenerateAbout(preferredGenres)

		// Crear documento
		doc := models.UserDoc{
			UserID:          uid,
			FirstName:       firstName,
			LastName:        lastName,
			Username:        username,
			Email:           email,
			PasswordHash:    passwordHash,
			Role:            "user",
			About:           about,
			PreferredGenres: preferredGenres,
			CreatedAt:       now,
			UpdatedAt:       now, // Inicialmente igual a CreatedAt
		}

		// Agregar uIdx usando el mapper dinámico
		uIdx := userMapper.GetOrCreate(uid)
		doc.UIdx = &uIdx

		// Escribir NDJSON
		b, _ := json.Marshal(doc)
		w.Write(b)
		w.WriteByte('\n')

		// Escribir log
		uIdxStr := "null"
		if doc.UIdx != nil {
			uIdxStr = fmt.Sprintf("%d", *doc.UIdx)
		}
		logWriter.WriteString(fmt.Sprintf("%d,%s,%s,%s,%s,%s,%s,%s\n",
			uid, uIdxStr, firstName, lastName, username, email, password, passwordHash))

		written++
	}

	return written, nil
}

// ProcessSimilarities genera similarities.ndjson
func ProcessSimilarities(outPath string, similarities map[int][]models.Neighbor, itemMapper *mappers.IDMapper) (int, error) {
	// Crear reverse map: iIdx -> movieId
	itemMap := itemMapper.GetMapping()
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

		doc := models.SimilarityDoc{
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

// ProcessMovies genera movies.ndjson enriquecido con datos externos
func ProcessMovies(inPath, outPath string, links map[int]*models.Links, genomeTags map[int][]models.GenomeTag, userTags map[int][]string, ratingStats map[int]*models.RatingStats, itemMapper *mappers.IDMapper, topGenomeTags int, tmdbClient *external.TMDBClient, fetchExternal bool, yearRe *regexp.Regexp) (int, error) {
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

		title, year := parseTitleAndYear(titleRaw, yearRe)
		genres := []string{}
		if genresRaw != "" && genresRaw != "(no genres listed)" {
			for _, g := range strings.Split(genresRaw, "|") {
				g = strings.TrimSpace(g)
				if g != "" {
					genres = append(genres, g)
				}
			}
		}

		doc := models.MovieDoc{
			MovieID:   mid,
			Title:     title,
			Year:      year,
			Genres:    genres,
			CreatedAt: now,
			UpdatedAt: now,
		}

		// Agregar iIdx usando el mapper dinámico
		iIdx := itemMapper.GetOrCreate(mid)
		doc.IIdx = &iIdx

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

// ProcessRatings genera ratings.ndjson
func ProcessRatings(inPath, outPath string) (int, error) {
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

		doc := models.RatingDoc{
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
