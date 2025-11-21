# Guía de Uso - ETL Construction with MongoDB

Guía paso a paso para configurar, ejecutar y verificar el sistema ETL para MovieLens con MongoDB.

---

## 📑 Tabla de Contenidos

- [Requisitos Previos](#-requisitos-previos)
- [Estructura de Archivos Requeridos](#-estructura-de-archivos-requeridos)
- [Configuración Inicial](#-configuración-inicial)
- [Ejecución del ETL](#-ejecución-del-etl)
- [Importación a MongoDB](#-importación-a-mongodb)
- [Verificación de Datos](#-verificación-de-datos)
- [Solución de Problemas](#-solución-de-problemas)

---

## 🔧 Requisitos Previos

### Software Necesario

1. **Go 1.21+**
   ```powershell
   # Verificar instalación
   go version
   # Salida esperada: go version go1.21.x windows/amd64
   ```
   - Descargar: https://go.dev/dl/

2. **MongoDB 4.4+**
   ```powershell
   # Verificar instalación
   mongod --version
   # Salida esperada: db version v4.4.x
   ```
   - Descargar: https://www.mongodb.com/try/download/community

3. **Git** (opcional, para clonar el repositorio)
   ```powershell
   git --version
   ```

### Cuenta TMDB (Opcional - Solo para Fase 2)

Si deseas enriquecer las películas con datos externos (posters, cast, sinopsis):

1. Crea una cuenta en: https://www.themoviedb.org/signup
2. Ve a: https://www.themoviedb.org/settings/api
3. Solicita una **API Key** (gratuita para uso personal)
4. Copia tu API Key (formato: `5f947eefe9278165015da465d0af58c3`)

---

## 📂 Estructura de Archivos Requeridos

### Árbol de Directorios

```
PC4_ETLConstructionWithMongoDB/
├── main.go                          # Programa principal del ETL
├── go.mod                           # Dependencias de Go
├── go.sum                           # Checksums de dependencias
├── .env                             # Configuración de API keys
├── .env.example                     # Plantilla de configuración
├── .gitignore                       # Archivos ignorados por git
├── README.md                        # Documentación del proyecto
├── GUIDE.md                         # Esta guía
├── FORMATO_MOVIE_EJEMPLO.txt        # Ejemplo de documento movie
├── data/                            # ⚠️ ARCHIVOS CSV REQUERIDOS
│   ├── movies.csv                   # [REQUERIDO] Películas principales
│   ├── ratings.csv                  # [REQUERIDO] Valoraciones de usuarios
│   ├── links.csv                    # [REQUERIDO] Enlaces a IMDB/TMDB
│   ├── tags.csv                     # [REQUERIDO] Tags de usuarios
│   ├── genome-tags.csv              # [REQUERIDO] Tags del sistema genome
│   ├── genome-scores.csv            # [REQUERIDO] Relevancia de genome tags
│   ├── item_map.csv                 # [REQUERIDO] Mapeo movieId -> iIdx
│   ├── user_map.csv                 # [REQUERIDO] Mapeo userId -> uIdx
│   ├── item_topk_cosine_conc.csv    # [REQUERIDO] Similitudes pre-calculadas
│   ├── movies_test.csv              # [OPCIONAL] Dataset de prueba (10 películas)
│   └── ratings_test.csv             # [OPCIONAL] Ratings de prueba
└── out/                             # Archivos NDJSON generados (auto-creado)
    ├── movies.ndjson
    ├── ratings.ndjson
    ├── users.ndjson
    ├── similarities.ndjson
    ├── passwords_log.csv
    └── report.txt                   # Reporte de ejecución
```

### Obtener los Archivos CSV

#### Opción 1: Dataset MovieLens 25M (Completo)

```powershell
# Descargar dataset completo (~265 MB comprimido)
Invoke-WebRequest -Uri "https://files.grouplens.org/datasets/movielens/ml-25m.zip" -OutFile "ml-25m.zip"

# Extraer archivos
Expand-Archive -Path "ml-25m.zip" -DestinationPath "."

# Copiar archivos necesarios a la carpeta data/
Copy-Item "ml-25m\movies.csv" -Destination "data\"
Copy-Item "ml-25m\ratings.csv" -Destination "data\"
Copy-Item "ml-25m\links.csv" -Destination "data\"
Copy-Item "ml-25m\tags.csv" -Destination "data\"
Copy-Item "ml-25m\genome-tags.csv" -Destination "data\"
Copy-Item "ml-25m\genome-scores.csv" -Destination "data\"
```

#### Opción 2: Archivos Proporcionados por el Equipo

Los archivos `item_map.csv`, `user_map.csv` e `item_topk_cosine_conc.csv` son generados por el equipo de recomendaciones. Solicítalos a tus compañeros o verifica el repositorio compartido del proyecto.

### Verificar Archivos

```powershell
# Listar archivos en data/
Get-ChildItem data\ | Select-Object Name, Length

# Salida esperada:
# Name                          Length
# ----                          ------
# genome-scores.csv          79156932
# genome-tags.csv               16606
# item_map.csv                 589440
# item_topk_cosine_conc.csv  24560123
# links.csv                   2144893
# movies.csv                  2695599
# ratings.csv                776773325
# tags.csv                    2092781
# user_map.csv                3093574
```

---

## ⚙️ Configuración Inicial

### 1. Clonar el Repositorio

```powershell
git clone https://github.com/PrograCyD/PC4_ETLConstructionWithMongoDB.git
cd PC4_ETLConstructionWithMongoDB
```

### 2. Instalar Dependencias de Go

```powershell
# Inicializar módulo de Go (si no existe go.mod)
go mod init pc4_etl

# Instalar dependencias
go mod tidy

# Salida esperada:
# go: finding module for package golang.org/x/crypto/bcrypt
# go: found golang.org/x/crypto/bcrypt in golang.org/x/crypto v0.45.0
```

### 3. Configurar API Key de TMDB (Opcional)

**Opción A: Archivo .env (Recomendado)**

```powershell
# Copiar plantilla
Copy-Item .env.example .env

# Editar .env con tu API key
notepad .env
```

Contenido de `.env`:
```env
# TMDB API Configuration
TMDB_API_KEY=tu_api_key_aqui
```

**Opción B: Flag en línea de comandos**

```powershell
go run main.go --fetch-external --tmdb-api-key="tu_api_key_aqui"
```

### 4. Verificar Configuración

```powershell
# Probar que Go puede leer el código
go build -o etl.exe main.go

# Si compila correctamente, verás etl.exe en el directorio
Get-ChildItem *.exe
```

---

## 🚀 Ejecución del ETL

### Modo 1: Prueba Rápida (10 películas)

**Recomendado para verificar que todo funciona correctamente.**

```powershell
# Fase 1: Solo datos locales (~5 segundos)
go run main.go --movies-file movies_test.csv

# Fase 2: Con datos externos de TMDB (~10 segundos)
go run main.go --movies-file movies_test.csv --fetch-external
```

**Salida esperada:**
```
✓ Archivo .env cargado
=== ETL para MongoDB - Fase 2 (con datos externos de TMDB) ===

✓ Cliente TMDB inicializado (rate limit: 4 req/s)

Cargando links...
  ✓ 62423 links cargados
Cargando genome tags...
  ✓ 1128 genome tags cargados
Cargando genome scores...
  ✓ Genome scores cargados para 13816 películas (relevancia >= 0.50)
Cargando user tags...
  ✓ User tags cargados para 45251 películas
Calculando estadísticas de ratings...
  ✓ Estadísticas calculadas para 59047 películas
Cargando mapeo de items...
  ✓ Mapeo de items cargado para 32720 películas
Cargando mapeo de usuarios...
  ✓ Mapeo de usuarios cargado para 162541 usuarios

Procesando movies con datos externos de TMDB: data\movies_test.csv
  ⏳ Esto puede tardar varios minutos debido al rate limiting...
  ✓ 10 películas enriquecidas con datos de TMDB
  ✓ Escritas 10 películas en out\movies.ndjson

Procesando ratings: data\ratings.csv
  ✓ Escritas 25000095 entradas en out\ratings.ndjson

Generando users con passwords hasheados...
  ✓ Generados 162541 usuarios en out\users.ndjson
  ⚠ Passwords sin hashear (modo rápido)
  ✓ Log de passwords guardado en out\passwords_log.csv

Cargando similitudes desde data\item_topk_cosine_conc.csv ...
  ✓ Similitudes cargadas para 30202 películas
Generando similarities...
  ✓ Generadas 30202 entradas de similitud en out\similarities.ndjson

  ✓ Reporte generado en out\report.txt

=== ETL completado exitosamente ===
Tiempo total de ejecución: 5s
```

### Modo 2: Dataset Completo (Producción)

**Tiempo estimado: 5-7 minutos (Fase 1) o 4-5 horas (Fase 2)**

```powershell
# Fase 1: Solo datos locales, sin hashing (RÁPIDO - 5 segundos)
go run main.go --hash-passwords=false

# Fase 1: Solo datos locales, con hashing (LENTO - 10 minutos)
go run main.go --hash-passwords=true

# Fase 2: Con TMDB API, sin hashing (LENTO - 4-5 horas)
go run main.go --fetch-external --hash-passwords=false

# Fase 2: Con TMDB API, con hashing (MUY LENTO - 4-5 horas + 10 min)
go run main.go --fetch-external --hash-passwords=true
```

**⚠️ Importante para Fase 2:**
- El rate limiting de TMDB (4 req/s) hace que procesar 62K películas tome ~4-5 horas
- No interrumpir el proceso (puedes reanudar, hay caché en memoria)
- Considera ejecutar en horarios fuera de trabajo

### Opciones de Configuración

| Flag | Descripción | Valor por Defecto |
|------|-------------|-------------------|
| `--data-dir` | Directorio con CSVs de entrada | `data` |
| `--movies-file` | Nombre del archivo de películas | `movies.csv` |
| `--ratings-file` | Nombre del archivo de ratings | `ratings.csv` |
| `--out-dir` | Directorio de salida NDJSON | `out` |
| `--min-relevance` | Relevancia mínima para genome tags | `0.5` |
| `--top-genome-tags` | Top N genome tags por película | `10` |
| `--hash-passwords` | Hashear passwords con bcrypt | `true` |
| `--update-mappings` | Actualizar item_map.csv y user_map.csv con nuevos IDs | `false` |
| `--fetch-external` | Obtener datos de TMDB API | `false` |
| `--tmdb-api-key` | API key de TMDB | (lee de .env) |
| `--tmdb-rate-limit` | Requests/segundo a TMDB | `4` |

**Ejemplos de uso:**

```powershell
# Procesar solo 5000 películas más relevantes
go run main.go --movies-file movies_top5000.csv

# Cambiar directorio de salida
go run main.go --out-dir output_produccion

# Incrementar genome tags por película
go run main.go --top-genome-tags 20

# Hashear passwords (producción)
go run main.go --hash-passwords=true

# Sin hashear passwords (desarrollo rápido)
go run main.go --hash-passwords=false
```

---

## 📥 Importación a MongoDB

### 1. Iniciar MongoDB

```powershell
# Iniciar servidor MongoDB
mongod --dbpath C:\data\db

# En otra terminal, abrir shell de MongoDB
mongosh
```

### 2. Crear Base de Datos

```javascript
// En mongosh
use movielens

// Verificar que estás en la DB correcta
db.getName()
// Salida: movielens
```

### 3. Importar Colecciones

**Desde PowerShell (otra terminal):**

```powershell
# Variables de configuración
$DB = "movielens"
$OUT_DIR = "out"

# Importar movies
mongoimport --db $DB --collection movies --file "$OUT_DIR\movies.ndjson" --jsonArray=false
# Tiempo: ~10 segundos
# Salida: 62423 documentos importados

# Importar ratings
mongoimport --db $DB --collection ratings --file "$OUT_DIR\ratings.ndjson" --jsonArray=false
# Tiempo: ~2-3 minutos
# Salida: 25000095 documentos importados

# Importar users
mongoimport --db $DB --collection users --file "$OUT_DIR\users.ndjson" --jsonArray=false
# Tiempo: ~5 segundos
# Salida: 162541 documentos importados

# Importar similarities
mongoimport --db $DB --collection similarities --file "$OUT_DIR\similarities.ndjson" --jsonArray=false
# Tiempo: ~10 segundos
# Salida: 30202 documentos importados
```

### 4. Crear Índices (Opcional pero Recomendado)

**Desde mongosh:**

```javascript
use movielens

// Índices para movies
db.movies.createIndex({ movieId: 1 })
db.movies.createIndex({ iIdx: 1 })
db.movies.createIndex({ title: "text" })
db.movies.createIndex({ "ratingStats.average": -1 })

// Índices para ratings
db.ratings.createIndex({ userId: 1, movieId: 1 })
db.ratings.createIndex({ movieId: 1 })
db.ratings.createIndex({ userId: 1 })

// Índices para users
db.users.createIndex({ userId: 1 }, { unique: true })
db.users.createIndex({ uIdx: 1 })
db.users.createIndex({ email: 1 }, { unique: true })

// Índices para similarities
db.similarities.createIndex({ iIdx: 1 })
db.similarities.createIndex({ movieId: 1 })

// Verificar índices
db.movies.getIndexes()
```

---

## ✅ Verificación de Datos

### 1. Verificar Conteos

```javascript
// En mongosh
use movielens

// Contar documentos por colección
db.movies.countDocuments()      // Esperado: 62423
db.ratings.countDocuments()     // Esperado: 25000095
db.users.countDocuments()       // Esperado: 162541
db.similarities.countDocuments() // Esperado: 30202
```

### 2. Inspeccionar Documentos de Ejemplo

```javascript
// Ver primera película con todos sus datos
db.movies.findOne({ movieId: 1 })

// Ver película con datos externos
db.movies.findOne(
  { "externalData.tmdbFetched": true },
  { title: 1, "externalData.posterUrl": 1, "externalData.cast": 1 }
)

// Ver usuario con mapeo
db.users.findOne({ userId: 1 })

// Ver similitudes de una película
db.similarities.findOne({ movieId: 1 })
```

### 3. Consultas de Validación

```javascript
// Top 10 películas mejor valoradas (con al menos 1000 ratings)
db.movies.find(
  { "ratingStats.count": { $gte: 1000 } }
).sort({ "ratingStats.average": -1 }).limit(10)

// Películas con datos externos
db.movies.countDocuments({ "externalData.tmdbFetched": true })

// Usuarios con mapeo uIdx
db.users.countDocuments({ uIdx: { $exists: true } })

// Verificar que todas las similitudes tienen 20 vecinos (o menos)
db.similarities.aggregate([
  {
    $project: {
      movieId: 1,
      neighborsCount: { $size: "$neighbors" },
      k: 1
    }
  },
  { $match: { $expr: { $lte: ["$neighborsCount", "$k"] } } }
]).toArray()
```

### 4. Verificar Integridad de Datos

```javascript
// Verificar que todos los movieId tienen iIdx
db.movies.countDocuments({ iIdx: { $exists: false } })
// Esperado: 0 (o alguno si no está en item_map.csv)

// Verificar que todos los userId tienen uIdx
db.users.countDocuments({ uIdx: { $exists: false } })
// Esperado: 0 (o alguno si no está en user_map.csv)

// Verificar formato de emails
db.users.findOne({ email: { $not: /^user\d+@email\.com$/ } })
// Esperado: null (todos siguen el formato)

// Verificar que genomeTags no exceden 10
db.movies.aggregate([
  {
    $project: {
      movieId: 1,
      genomeTagsCount: { $size: { $ifNull: ["$genomeTags", []] } }
    }
  },
  { $match: { genomeTagsCount: { $gt: 10 } } }
]).toArray()
// Esperado: [] (vacío)

// Verificar que userTags no exceden 10
db.movies.aggregate([
  {
    $project: {
      movieId: 1,
      userTagsCount: { $size: { $ifNull: ["$userTags", []] } }
    }
  },
  { $match: { userTagsCount: { $gt: 10 } } }
]).toArray()
// Esperado: [] (vacío)
```

---

## 🐛 Solución de Problemas

### Problema 1: Error "cannot find package golang.org/x/crypto/bcrypt"

**Solución:**
```powershell
go mod init pc4_etl
go mod tidy
```

### Problema 2: Error "no such file or directory: data/movies.csv"

**Causa:** Archivos CSV no están en la carpeta `data/`

**Solución:**
```powershell
# Verificar archivos
Get-ChildItem data\

# Si no existen, descargar dataset MovieLens (ver sección anterior)
```

### Problema 3: "Error: --fetch-external requiere --tmdb-api-key"

**Causa:** No se configuró la API key de TMDB

**Solución:**
```powershell
# Crear archivo .env
Copy-Item .env.example .env
notepad .env
# Agregar: TMDB_API_KEY=tu_api_key_aqui
```

### Problema 4: ETL muy lento en Fase 2

**Causa:** Rate limiting de TMDB (4 req/s para 62K películas = ~4.3 horas)

**Soluciones:**
- **Opción 1:** Ejecutar solo Fase 1 (sin `--fetch-external`)
- **Opción 2:** Usar `movies_test.csv` para pruebas rápidas
- **Opción 3:** Dejar corriendo en segundo plano

```powershell
# Ejecutar en background (PowerShell 7+)
Start-Job -ScriptBlock { go run main.go --fetch-external }

# Verificar progreso
Get-Job | Receive-Job -Keep
```

### Problema 5: MongoDB no puede importar archivos

**Causa:** Formato NDJSON incorrecto o MongoDB no está corriendo

**Solución:**
```powershell
# Verificar que MongoDB está corriendo
Get-Process mongod

# Verificar formato NDJSON
Get-Content out\movies.ndjson -Head 1 | ConvertFrom-Json

# Re-importar con verbose
mongoimport --db movielens --collection movies --file out\movies.ndjson --verbose
```

### Problema 6: Passwords muy lentos de hashear

**Causa:** bcrypt es computacionalmente costoso (162K usuarios × 1024 iteraciones)

**Solución:**
```powershell
# Deshabilitar hashing para desarrollo
go run main.go --hash-passwords=false

# Habilitar solo para producción
go run main.go --hash-passwords=true
```

**Nota:** Los passwords sin hashear estarán en `out/passwords_log.csv` en ambos casos.

### Problema 7: Error de memoria (Out of Memory)

**Causa:** Procesamiento de 25M ratings puede consumir mucha RAM

**Soluciones:**
- Cerrar aplicaciones innecesarias
- Aumentar swap/paging en Windows
- Procesar en lotes (crear subsets de ratings.csv)

### Problema 8: Archivos de salida vacíos

**Causa:** Errores durante el procesamiento no fueron reportados

**Solución:**
```powershell
# Ejecutar con output completo
go run main.go 2>&1 | Tee-Object -FilePath etl.log

# Revisar log
notepad etl.log
```

---

## 📞 Soporte

### Recursos Adicionales

- **README.md**: Teoría y arquitectura del proyecto
- **FORMATO_MOVIE_EJEMPLO.txt**: Ejemplo visual de documento movie
- **Repositorio**: https://github.com/PrograCyD/PC4_ETLConstructionWithMongoDB
- **Issues**: Reportar problemas en GitHub Issues

### Comandos Útiles de Diagnóstico

```powershell
# Verificar versiones
go version
mongod --version
mongoimport --version

# Verificar espacio en disco
Get-PSDrive C | Select-Object Used,Free

# Verificar memoria disponible
Get-CimInstance Win32_OperatingSystem | Select-Object FreePhysicalMemory

# Ver procesos de Go/MongoDB corriendo
Get-Process | Where-Object { $_.ProcessName -match "go|mongo" }

# Limpiar archivos de salida anteriores
Remove-Item out\*.ndjson, out\*.csv -Force
```

---

## ✨ Siguientes Pasos

Una vez completada la importación:

1. **Revisar reporte**: Consulta `out/report.txt` para ver estadísticas y tiempos
2. **Explorar datos**: Usa mongosh o MongoDB Compass
3. **Integrar con backend**: Conectar API REST a la base de datos
4. **Implementar búsquedas**: Usar índices de texto y agregaciones
5. **Sistema de recomendaciones**: Consumir colección `similarities`
6. **Autenticación**: Usar colección `users` y `passwords_log.csv`

**¡El ETL está listo para alimentar tu sistema de recomendaciones!** 🎬🍿
