package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"pc4_etl/internal/external"
	"pc4_etl/internal/loaders"
	"pc4_etl/internal/mappers"
	"pc4_etl/internal/models"
	"pc4_etl/internal/processors"
	"pc4_etl/internal/utils"
)

var yearRe = regexp.MustCompile(`\((\d{4})\)\s*$`)

func main() {
	startTime := time.Now()

	// Intentar cargar .env antes de parsear flags
	if err := utils.LoadEnvFile(".env"); err == nil {
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

	// Flags para ejecución selectiva de procesadores
	processMovies := flag.Bool("process-movies", true, "Si es true, genera movies.ndjson")
	processRatings := flag.Bool("process-ratings", true, "Si es true, genera ratings.ndjson")
	processUsers := flag.Bool("process-users", true, "Si es true, genera users.ndjson")
	processSimilarities := flag.Bool("process-similarities", true, "Si es true, genera similarities.ndjson")

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

	// Cargar datos complementarios (solo si son necesarios)
	var links map[int]*models.Links
	var genomeScores map[int][]models.GenomeTag
	var userTags map[int][]string
	var ratingStats map[int]*models.RatingStats

	if *processMovies {
		fmt.Println("Cargando links...")
		var err error
		links, err = loaders.LoadLinks(linksPath)
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
		genomeScores, err = loaders.LoadGenomeScores(genomeScoresPath, genomeTagsMap, *minRelevance)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar genome-scores.csv: %v\n", err)
			genomeScores = make(map[int][]models.GenomeTag)
		}
		fmt.Printf("  ✓ Genome scores cargados para %d películas (relevancia >= %.2f)\n", len(genomeScores), *minRelevance)

		fmt.Println("Cargando user tags...")
		userTags, err = loaders.LoadUserTags(tagsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar tags.csv: %v\n", err)
			userTags = make(map[int][]string)
		}
		fmt.Printf("  ✓ User tags cargados para %d películas\n", len(userTags))

		fmt.Println("Calculando estadísticas de ratings...")
		ratingStats, err = loaders.LoadRatingStats(ratingsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar ratings.csv: %v\n", err)
			ratingStats = make(map[int]*models.RatingStats)
		}
		fmt.Printf("  ✓ Estadísticas calculadas para %d películas\n", len(ratingStats))
	}

	// Cargar mapeos (siempre necesarios si hay algún procesador activo)
	var itemMapper *mappers.IDMapper
	var userMapper *mappers.IDMapper

	if *processMovies || *processSimilarities {
		fmt.Println("Cargando mapeo de items...")
		itemMap, err := loaders.LoadItemMap(itemMapPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar item_map.csv: %v\n", err)
			itemMap = make(map[int]int)
		}
		fmt.Printf("  ✓ Mapeo de items cargado para %d películas\n", len(itemMap))
		itemMapper = mappers.NewIDMapper(itemMap)
	}

	if *processUsers {
		fmt.Println("Cargando mapeo de usuarios...")
		userMap, err := loaders.LoadUserMap(userMapPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar user_map.csv: %v\n", err)
			userMap = make(map[int]int)
		}
		fmt.Printf("  ✓ Mapeo de usuarios cargado para %d usuarios\n", len(userMap))
		userMapper = mappers.NewIDMapper(userMap)
	}

	// Cargar géneros únicos si se van a procesar usuarios
	var allGenres []string
	if *processUsers {
		fmt.Println("Extrayendo géneros únicos de movies.csv...")
		var err error
		allGenres, err = loaders.ExtractUniqueGenres(moviesPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Advertencia: no se pudieron extraer géneros: %v\n", err)
			allGenres = []string{"Action", "Adventure", "Comedy", "Drama", "Thriller"} // Fallback
		}
		fmt.Printf("  ✓ %d géneros únicos extraídos\n", len(allGenres))
	}

	// Procesar archivos según flags
	var mcount, rcount, ucount, scount int

	if *processMovies {
		fmt.Println()
		if *fetchExternal {
			fmt.Println("Procesando movies con datos externos de TMDB:", moviesPath)
			fmt.Println("  ⏳ Esto puede tardar varios minutos debido al rate limiting...")
		} else {
			fmt.Println("Procesando movies:", moviesPath)
		}
		var merr error
		mcount, merr = processors.ProcessMovies(moviesPath, moviesOut, links, genomeScores, userTags, ratingStats, itemMapper, *topGenomeTags, tmdbClient, *fetchExternal, yearRe)
		if merr != nil {
			fmt.Fprintln(os.Stderr, "error procesando movies:", merr)
			os.Exit(1)
		}
		fmt.Printf("  ✓ Escritas %d películas en %s\n", mcount, moviesOut)
	} else {
		fmt.Println()
		fmt.Println("⏭ Procesamiento de movies omitido (--process-movies=false)")
	}

	if *processRatings {
		fmt.Println()
		fmt.Println("Procesando ratings:", ratingsPath)
		var rerr error
		rcount, rerr = processors.ProcessRatings(ratingsPath, ratingsOut)
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "error procesando ratings:", rerr)
			os.Exit(1)
		}
		fmt.Printf("  ✓ Escritas %d entradas en %s\n", rcount, ratingsOut)
	} else {
		fmt.Println()
		fmt.Println("⏭ Procesamiento de ratings omitido (--process-ratings=false)")
	}

	if *processUsers {
		fmt.Println()
		fmt.Println("Generando users con passwords hasheados...")
		var uerr error
		ucount, uerr = processors.ProcessUsers(ratingsPath, usersOut, passwordLogOut, userMapper, *hashPasswords, allGenres)
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
	} else {
		fmt.Println()
		fmt.Println("⏭ Procesamiento de users omitido (--process-users=false)")
	}

	if *processSimilarities {
		fmt.Println()
		fmt.Println("Cargando similitudes desde", similaritiesPath, "...")
		similarities, serr := loaders.LoadSimilarities(similaritiesPath, itemMapper)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "Advertencia: no se pudo cargar similitudes: %v\n", serr)
			similarities = make(map[int][]models.Neighbor)
		}
		fmt.Printf("  ✓ Similitudes cargadas para %d películas\n", len(similarities))

		fmt.Println("Generando similarities...")
		var serr2 error
		scount, serr2 = processors.ProcessSimilarities(similaritiesOut, similarities, itemMapper)
		if serr2 != nil {
			fmt.Fprintln(os.Stderr, "error generando similarities:", serr2)
			os.Exit(1)
		}
		fmt.Printf("  ✓ Generadas %d entradas de similitud en %s\n", scount, similaritiesOut)
	} else {
		fmt.Println()
		fmt.Println("⏭ Procesamiento de similarities omitido (--process-similarities=false)")
	}

	// Persistir mapeos si fueron modificados y el flag está activo
	if *updateMappings {
		if itemMapper != nil && itemMapper.HasChanged() {
			fmt.Println()
			fmt.Println("Actualizando item_map.csv con nuevos movieIds...")
			if err := mappers.SaveItemMap(itemMapPath, itemMapper.GetMapping()); err != nil {
				fmt.Fprintf(os.Stderr, "Advertencia: no se pudo actualizar item_map.csv: %v\n", err)
			} else {
				fmt.Printf("  ✓ item_map.csv actualizado (%d películas)\n", itemMapper.Count())
			}
		}
		if userMapper != nil && userMapper.HasChanged() {
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
	if err := utils.GenerateReport(reportPath, mcount, rcount, ucount, scount, *hashPasswords, *fetchExternal, *processMovies, *processRatings, *processUsers, *processSimilarities, elapsedTime); err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo generar reporte: %v\n", err)
	} else {
		fmt.Printf("\n  ✓ Reporte generado en %s\n", reportPath)
	}

	fmt.Println()
	fmt.Println("=== ETL completado exitosamente ===")
	fmt.Printf("Tiempo total de ejecución: %s\n", utils.FormatDuration(elapsedTime))
}
