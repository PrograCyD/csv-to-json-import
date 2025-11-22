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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pc4_etl/internal/external"
	"pc4_etl/internal/loaders"
	"pc4_etl/internal/mappers"
	"pc4_etl/internal/models"

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
func processUsers(ratingsPath, outPath, passwordLogPath string, userMapper *mappers.IDMapper, hashPasswords bool) (int, error) {
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
		doc := models.UserDoc{
			UserID:       uid,
			Email:        email,
			PasswordHash: passwordHash,
			Role:         "user",
			CreatedAt:    now,
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
		logWriter.WriteString(fmt.Sprintf("%d,%s,%s,%s,%s\n", uid, uIdxStr, email, password, passwordHash))

		written++
	}

	return written, nil
}

// processSimilarities genera similarities.ndjson
func processSimilarities(outPath string, similarities map[int][]models.Neighbor, itemMapper *mappers.IDMapper) (int, error) {
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

func processMovies(inPath, outPath string, links map[int]*models.Links, genomeTags map[int][]models.GenomeTag, userTags map[int][]string, ratingStats map[int]*models.RatingStats, itemMapper *mappers.IDMapper, topGenomeTags int, tmdbClient *external.TMDBClient, fetchExternal bool) (int, error) {
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

		doc := models.MovieDoc{
			MovieID:   mid,
			Title:     title,
			Year:      year,
			Genres:    genres,
			CreatedAt: now,
			UpdatedAt: now,
		} // Agregar iIdx usando el mapper dinámico
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

// formatDuration convierte una duración en un formato legible
func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	} else if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// generateReport genera un archivo de reporte con estadísticas del ETL
func generateReport(path string, moviesCount, ratingsCount, usersCount, similaritiesCount int, hashedPasswords, fetchedExternal bool, elapsed time.Duration) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	w := bufio.NewWriter(file)
	defer w.Flush()

	// Encabezado
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w, "               ETL CONSTRUCTION WITH MONGODB - REPORTE DE EJECUCIÓN")
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Fecha de ejecución: %s\n", time.Now().Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(w, "Tiempo total: %s\n", formatDuration(elapsed))
	fmt.Fprintln(w)

	// Configuración
	fmt.Fprintln(w, "CONFIGURACIÓN:")
	fmt.Fprintln(w, strings.Repeat("-", 80))
	if fetchedExternal {
		fmt.Fprintln(w, "  ✓ Fase 2: Datos enriquecidos con TMDB API")
	} else {
		fmt.Fprintln(w, "  • Fase 1: Solo datos locales (sin TMDB)")
	}
	if hashedPasswords {
		fmt.Fprintln(w, "  ✓ Passwords hasheados con bcrypt (seguro)")
	} else {
		fmt.Fprintln(w, "  ⚠ Passwords sin hashear (modo desarrollo)")
	}
	fmt.Fprintln(w)

	// Estadísticas
	fmt.Fprintln(w, "ESTADÍSTICAS DE DATOS PROCESADOS:")
	fmt.Fprintln(w, strings.Repeat("-", 80))
	fmt.Fprintf(w, "  Movies:        %10d documentos generados\n", moviesCount)
	fmt.Fprintf(w, "  Ratings:       %10d documentos generados\n", ratingsCount)
	fmt.Fprintf(w, "  Users:         %10d documentos generados\n", usersCount)
	fmt.Fprintf(w, "  Similarities:  %10d documentos generados\n", similaritiesCount)
	fmt.Fprintln(w, strings.Repeat("-", 80))
	total := moviesCount + ratingsCount + usersCount + similaritiesCount
	fmt.Fprintf(w, "  TOTAL:         %10d documentos\n", total)
	fmt.Fprintln(w)

	// Archivos generados
	fmt.Fprintln(w, "ARCHIVOS GENERADOS:")
	fmt.Fprintln(w, strings.Repeat("-", 80))
	fmt.Fprintln(w, "  • out/movies.ndjson         - Películas con metadata completa")
	fmt.Fprintln(w, "  • out/ratings.ndjson        - Valoraciones de usuarios")
	fmt.Fprintln(w, "  • out/users.ndjson          - Usuarios con credenciales")
	fmt.Fprintln(w, "  • out/similarities.ndjson   - Similitudes coseno (k=20)")
	fmt.Fprintln(w, "  • out/passwords_log.csv     - Log de passwords (desarrollo)")
	fmt.Fprintln(w, "  • out/report.txt            - Este reporte")
	fmt.Fprintln(w)

	// Comandos de importación
	fmt.Fprintln(w, "IMPORTACIÓN A MONGODB:")
	fmt.Fprintln(w, strings.Repeat("-", 80))
	fmt.Fprintln(w, "Ejecutar los siguientes comandos en PowerShell:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  $DB = \"movielens\"")
	fmt.Fprintln(w, "  $OUT_DIR = \"out\"")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  mongoimport --db $DB --collection movies --file \"$OUT_DIR\\movies.ndjson\"")
	fmt.Fprintln(w, "  mongoimport --db $DB --collection ratings --file \"$OUT_DIR\\ratings.ndjson\"")
	fmt.Fprintln(w, "  mongoimport --db $DB --collection users --file \"$OUT_DIR\\users.ndjson\"")
	fmt.Fprintln(w, "  mongoimport --db $DB --collection similarities --file \"$OUT_DIR\\similarities.ndjson\"")
	fmt.Fprintln(w)

	// Verificación
	fmt.Fprintln(w, "VERIFICACIÓN EN MONGODB:")
	fmt.Fprintln(w, strings.Repeat("-", 80))
	fmt.Fprintln(w, "Ejecutar en mongosh para verificar:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  use movielens")
	fmt.Fprintf(w, "  db.movies.countDocuments()       // Esperado: %d\n", moviesCount)
	fmt.Fprintf(w, "  db.ratings.countDocuments()      // Esperado: %d\n", ratingsCount)
	fmt.Fprintf(w, "  db.users.countDocuments()        // Esperado: %d\n", usersCount)
	fmt.Fprintf(w, "  db.similarities.countDocuments() // Esperado: %d\n", similaritiesCount)
	fmt.Fprintln(w)

	// Índices recomendados
	fmt.Fprintln(w, "ÍNDICES RECOMENDADOS:")
	fmt.Fprintln(w, strings.Repeat("-", 80))
	fmt.Fprintln(w, "  db.movies.createIndex({ movieId: 1 })")
	fmt.Fprintln(w, "  db.movies.createIndex({ iIdx: 1 })")
	fmt.Fprintln(w, "  db.movies.createIndex({ title: \"text\" })")
	fmt.Fprintln(w, "  db.ratings.createIndex({ userId: 1, movieId: 1 })")
	fmt.Fprintln(w, "  db.users.createIndex({ userId: 1 }, { unique: true })")
	fmt.Fprintln(w, "  db.users.createIndex({ email: 1 }, { unique: true })")
	fmt.Fprintln(w, "  db.similarities.createIndex({ iIdx: 1 })")
	fmt.Fprintln(w)

	// Notas finales
	fmt.Fprintln(w, "NOTAS:")
	fmt.Fprintln(w, strings.Repeat("-", 80))
	fmt.Fprintln(w, "  • Los IDs (iIdx, uIdx) son mapeos para optimización de modelos ML")
	fmt.Fprintln(w, "  • GenomeTags limitados a top 10 por relevancia (>= 0.5)")
	fmt.Fprintln(w, "  • UserTags limitados a top 10 por frecuencia de uso")
	if fetchedExternal {
		fmt.Fprintln(w, "  • Cast incluye profileUrl de TMDB (w185)")
		fmt.Fprintln(w, "  • Posters, sinopsis y runtime obtenidos de TMDB")
	}
	if !hashedPasswords {
		fmt.Fprintln(w, "  ⚠ IMPORTANTE: Passwords sin hashear - NO usar en producción")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Para más información consultar README.md y GUIDE.md")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w, "                          ¡ETL COMPLETADO EXITOSAMENTE!")
	fmt.Fprintln(w, "================================================================================")

	return nil
}

func main() {
	startTime := time.Now()

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
	updateMappings := flag.Bool("update-mappings", false, "Actualizar archivos item_map.csv y user_map.csv con nuevos IDs encontrados")

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
	var tmdbClient *external.TMDBClient
	if *fetchExternal {
		if *tmdbAPIKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --fetch-external requiere --tmdb-api-key")
			fmt.Fprintln(os.Stderr, "Obtén tu API key en: https://www.themoviedb.org/settings/api")
			os.Exit(1)
		}
		tmdbClient = external.NewTMDBClient(*tmdbAPIKey, *tmdbRateLimit)
		fmt.Printf("✓ Cliente TMDB inicializado (rate limit: %d req/s)\n", *tmdbRateLimit)
		fmt.Println()
	}

	// Cargar datos complementarios
	fmt.Println("Cargando links...")
	links, err := loaders.LoadLinks(linksPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar links.csv: %v\n", err)
		links = make(map[int]*models.Links)
	}
	fmt.Printf("  ✓ %d links cargados\n", len(links))

	fmt.Println("Cargando genome tags...")
	genomeTagsMap, err := loaders.LoadGenomeTags(genomeTagsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar genome-tags.csv: %v\n", err)
		genomeTagsMap = make(map[int]string)
	}
	fmt.Printf("  ✓ %d genome tags cargados\n", len(genomeTagsMap))

	fmt.Println("Cargando genome scores...")
	genomeScores, err := loaders.LoadGenomeScores(genomeScoresPath, genomeTagsMap, *minRelevance)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar genome-scores.csv: %v\n", err)
		genomeScores = make(map[int][]models.GenomeTag)
	}
	fmt.Printf("  ✓ Genome scores cargados para %d películas (relevancia >= %.2f)\n", len(genomeScores), *minRelevance)

	fmt.Println("Cargando user tags...")
	userTags, err := loaders.LoadUserTags(tagsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar tags.csv: %v\n", err)
		userTags = make(map[int][]string)
	}
	fmt.Printf("  ✓ User tags cargados para %d películas\n", len(userTags))

	fmt.Println("Calculando estadísticas de ratings...")
	ratingStats, err := loaders.LoadRatingStats(ratingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar ratings.csv: %v\n", err)
		ratingStats = make(map[int]*models.RatingStats)
	}
	fmt.Printf("  ✓ Estadísticas calculadas para %d películas\n", len(ratingStats))

	fmt.Println("Cargando mapeo de items...")
	itemMap, err := loaders.LoadItemMap(itemMapPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar item_map.csv: %v\n", err)
		itemMap = make(map[int]int)
	}
	fmt.Printf("  ✓ Mapeo de items cargado para %d películas\n", len(itemMap))
	itemMapper := mappers.NewIDMapper(itemMap)

	fmt.Println("Cargando mapeo de usuarios...")
	userMap, err := loaders.LoadUserMap(userMapPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar user_map.csv: %v\n", err)
		userMap = make(map[int]int)
	}
	fmt.Printf("  ✓ Mapeo de usuarios cargado para %d usuarios\n", len(userMap))
	userMapper := mappers.NewIDMapper(userMap)

	fmt.Println()
	if *fetchExternal {
		fmt.Println("Procesando movies con datos externos de TMDB:", moviesPath)
		fmt.Println("  ⏳ Esto puede tardar varios minutos debido al rate limiting...")
	} else {
		fmt.Println("Procesando movies:", moviesPath)
	}
	mcount, merr := processMovies(moviesPath, moviesOut, links, genomeScores, userTags, ratingStats, itemMapper, *topGenomeTags, tmdbClient, *fetchExternal)
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
	ucount, uerr := processUsers(ratingsPath, usersOut, passwordLogOut, userMapper, *hashPasswords)
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
	similarities, serr := loaders.LoadSimilarities(similaritiesPath, itemMapper)
	if serr != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar similitudes: %v\n", serr)
		similarities = make(map[int][]models.Neighbor)
	}
	fmt.Printf("  ✓ Similitudes cargadas para %d películas\n", len(similarities))

	fmt.Println("Generando similarities...")
	scount, serr2 := processSimilarities(similaritiesOut, similarities, itemMapper)
	if serr2 != nil {
		fmt.Fprintln(os.Stderr, "error generando similarities:", serr2)
		os.Exit(1)
	}
	fmt.Printf("  ✓ Generadas %d entradas de similitud en %s\n", scount, similaritiesOut)

	// Persistir mapeos si fueron modificados y el flag está activo
	if *updateMappings {
		if itemMapper.HasChanged() {
			fmt.Println()
			fmt.Println("Actualizando item_map.csv con nuevos movieIds...")
			if err := mappers.SaveItemMap(itemMapPath, itemMapper.GetMapping()); err != nil {
				fmt.Fprintf(os.Stderr, "Advertencia: no se pudo actualizar item_map.csv: %v\n", err)
			} else {
				fmt.Printf("  ✓ item_map.csv actualizado (%d películas)\n", itemMapper.Count())
			}
		}
		if userMapper.HasChanged() {
			fmt.Println("Actualizando user_map.csv con nuevos userIds...")
			if err := mappers.SaveUserMap(userMapPath, userMapper.GetMapping()); err != nil {
				fmt.Fprintf(os.Stderr, "Advertencia: no se pudo actualizar user_map.csv: %v\n", err)
			} else {
				fmt.Printf("  ✓ user_map.csv actualizado (%d usuarios)\n", userMapper.Count())
			}
		}
	}

	// Generar reporte final
	elapsedTime := time.Since(startTime)
	reportPath := filepath.Join(*outDir, "report.txt")
	if err := generateReport(reportPath, mcount, rcount, ucount, scount, *hashPasswords, *fetchExternal, elapsedTime); err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo generar reporte: %v\n", err)
	} else {
		fmt.Printf("\n  ✓ Reporte generado en %s\n", reportPath)
	}

	fmt.Println()
	fmt.Println("=== ETL completado exitosamente ===")
	fmt.Printf("Tiempo total de ejecución: %s\n", formatDuration(elapsedTime))
}
