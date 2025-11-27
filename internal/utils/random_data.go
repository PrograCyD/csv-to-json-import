package utils

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/jaswdr/faker"
)

var (
	// Templates para About con géneros (70%)
	aboutTemplatesWithGenres = []string{
		"Fan of %s movies",
		"I really like %s films",
		"Passionate about %s cinema",
		"Love watching %s movies",
		"Enthusiast of %s genre",
		"Big fan of %s",
		"Enjoys %s and more",
		"%s movies are my favorite",
		"Always up for %s films",
		"Can't get enough of %s",
	}

	// Frases simples para About (30%)
	simpleAbouts = []string{
		"Movie lover",
		"Film enthusiast",
		"Cinema addict",
		"Just here for the popcorn",
		"Passionate about cinema",
		"Movie buff",
		"Film fanatic",
		"Love watching movies",
		"Always looking for good films",
		"Cinema is my passion",
	}
)

// GenerateRandomName genera un nombre y apellido aleatorio usando faker
func GenerateRandomName(fake faker.Faker) (firstName, lastName string) {
	firstName = fake.Person().FirstName()
	lastName = fake.Person().LastName()
	return
}

// GenerateUsername genera un username basado en nombre, apellido y uid
// Formato: firstname.lastname + número (siempre con número)
func GenerateUsername(firstName, lastName string, uid int) string {
	base := fmt.Sprintf("%s.%s",
		strings.ToLower(firstName),
		strings.ToLower(lastName))

	// Añadir número basado en uid para unicidad
	number := uid % 10000
	return fmt.Sprintf("%s%d", base, number)
}

// GenerateAbout genera una descripción "about" para el usuario
// 70% con géneros favoritos, 30% frases simples
func GenerateAbout(preferredGenres []string) string {
	// 30% de probabilidad de usar frase simple
	if rand.Intn(10) < 3 {
		return simpleAbouts[rand.Intn(len(simpleAbouts))]
	}

	// 70% de probabilidad de usar géneros
	if len(preferredGenres) == 0 {
		// Si no hay géneros, usar frase simple
		return simpleAbouts[rand.Intn(len(simpleAbouts))]
	}

	// Seleccionar template aleatorio
	template := aboutTemplatesWithGenres[rand.Intn(len(aboutTemplatesWithGenres))]

	// Formatear con géneros
	var genreText string
	if len(preferredGenres) == 1 {
		genreText = preferredGenres[0]
	} else if len(preferredGenres) == 2 {
		genreText = fmt.Sprintf("%s and %s", preferredGenres[0], preferredGenres[1])
	} else {
		// Para 3 o más géneros, usar los primeros 2
		genreText = fmt.Sprintf("%s and %s", preferredGenres[0], preferredGenres[1])
	}

	return fmt.Sprintf(template, genreText)
}

// SelectRandomGenres selecciona entre 1 y 5 géneros aleatorios de una lista
func SelectRandomGenres(allGenres []string) []string {
	if len(allGenres) == 0 {
		return []string{}
	}

	// Número de géneros a seleccionar (1-5)
	count := rand.Intn(5) + 1
	if count > len(allGenres) {
		count = len(allGenres)
	}

	// Crear una copia para no modificar el original
	genresCopy := make([]string, len(allGenres))
	copy(genresCopy, allGenres)

	// Mezclar usando Fisher-Yates shuffle
	for i := len(genresCopy) - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		genresCopy[i], genresCopy[j] = genresCopy[j], genresCopy[i]
	}

	// Retornar los primeros 'count' géneros
	return genresCopy[:count]
}
