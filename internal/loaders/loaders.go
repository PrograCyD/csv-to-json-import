package loaders

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pc4_etl/internal/mappers"
	"pc4_etl/internal/models"
)

// LoadLinks carga los links desde links.csv
func LoadLinks(path string) (map[int]*models.Links, error) {
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

	links := make(map[int]*models.Links)
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

		link := &models.Links{}
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

// LoadGenomeTags carga el mapeo de tagId -> tag
func LoadGenomeTags(path string) (map[int]string, error) {
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

// LoadGenomeScores carga los scores de relevancia (movieId -> tagId -> relevance)
func LoadGenomeScores(path string, genomeTagsMap map[int]string, minRelevance float64) (map[int][]models.GenomeTag, error) {
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

	scores := make(map[int][]models.GenomeTag)
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
				scores[movieId] = append(scores[movieId], models.GenomeTag{
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

// LoadUserTags carga los tags de usuarios con frecuencia (movieId -> []tag ordenados por popularidad)
func LoadUserTags(path string) (map[int][]string, error) {
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
		tagList := make([]models.UserTagWithFrequency, 0, len(tags))
		for tag, users := range tags {
			tagList = append(tagList, models.UserTagWithFrequency{
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

// LoadRatingStats calcula estadísticas de ratings (movieId -> stats)
func LoadRatingStats(path string) (map[int]*models.RatingStats, error) {
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
	stats := make(map[int]*models.RatingStats)
	for movieId, acc := range accums {
		if acc.count > 0 {
			avg := acc.sum / float64(acc.count)
			lastRatedAt := ""
			if acc.lastTs > 0 {
				lastRatedAt = time.Unix(acc.lastTs, 0).UTC().Format(time.RFC3339)
			}
			stats[movieId] = &models.RatingStats{
				Average:     avg,
				Count:       acc.count,
				LastRatedAt: lastRatedAt,
			}
		}
	}

	return stats, nil
}

// LoadItemMap carga el mapeo movieId -> iIdx desde item_map.csv
func LoadItemMap(path string) (map[int]int, error) {
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

// LoadUserMap carga el mapeo userId -> uIdx desde user_map.csv
func LoadUserMap(path string) (map[int]int, error) {
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

// LoadSimilarities carga las similitudes desde item_topk_cosine_conc.csv
func LoadSimilarities(path string, itemMapper *mappers.IDMapper) (map[int][]models.Neighbor, error) {
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
	itemMap := itemMapper.GetMapping()
	reverseMap := make(map[int]int)
	for movieId, iIdx := range itemMap {
		reverseMap[iIdx] = movieId
	}

	// Agrupar por iIdx
	similarities := make(map[int][]models.Neighbor)
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

			neighbor := models.Neighbor{
				MovieID: jMovieId,
				IIdx:    jIdx,
				Sim:     sim,
			}
			similarities[iIdx] = append(similarities[iIdx], neighbor)
		}
	}

	return similarities, nil
}

// ExtractUniqueGenres extrae todos los géneros únicos del archivo movies.csv
func ExtractUniqueGenres(path string) ([]string, error) {
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

	genresMap := make(map[string]struct{})
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 3 {
			continue
		}

		// Los géneros están en la tercera columna, separados por |
		genresStr := strings.TrimSpace(rec[2])
		if genresStr == "" || genresStr == "(no genres listed)" {
			continue
		}

		// Dividir por | y agregar cada género
		genres := strings.Split(genresStr, "|")
		for _, genre := range genres {
			genre = strings.TrimSpace(genre)
			if genre != "" && genre != "(no genres listed)" {
				genresMap[genre] = struct{}{}
			}
		}
	}

	// Convertir map a slice y ordenar
	genresList := make([]string, 0, len(genresMap))
	for genre := range genresMap {
		genresList = append(genresList, genre)
	}
	sort.Strings(genresList)

	return genresList, nil
}
