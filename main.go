package main

import (
	"bytes"
	"database/sql"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ProcessingJob represents a media processing task
type ProcessingJob struct {
	ID       int64
	Path     string
	MimeType string
}

// Job queue channel
var jobQueue = make(chan ProcessingJob, 100)

func main() {
	initDB()
	r := gin.Default()
	r.Static("/static", "./static")
	r.Static("/storage", "./storage")

	r.LoadHTMLGlob("templates/*")

	r.GET("/", indexHandler)
	r.GET("/search", searchHandler)
	r.POST("/upload", uploadHandler)
	r.PUT("/media/:id/tags", updateTagsHandler)
	r.GET("/media/:id/status", getMediaStatusHandler)

	// Start background processing workers
	go startProcessingWorkers(3)

	log.Println("listening on http://localhost:8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}

// startProcessingWorkers starts the specified number of background workers
func startProcessingWorkers(numWorkers int) {
	for i := range numWorkers {
		go func(workerID int) {
			log.Printf("Starting processing worker %d", workerID)
			for job := range jobQueue {
				log.Printf("Worker %d processing job for media ID %d", workerID, job.ID)
				processMedia(job.ID, job.Path, job.MimeType)
			}
		}(i)
	}
}

func uploadHandler(c *gin.Context) {
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	files := form.File["files"]
	tags := c.PostForm("tags")

	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No files selected"})
		return
	}

	allowedTypes := map[string]bool{
		"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true,
		"video/mp4": true, "video/webm": true,
	}
	const maxSize = 10 << 20 // 10MB

	for _, file := range files {
		mimeType := mime.TypeByExtension(filepath.Ext(file.Filename))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		if !allowedTypes[mimeType] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported file type: " + mimeType})
			return
		}
		if file.Size > maxSize {
			c.JSON(http.StatusBadRequest, gin.H{"error": "File too large (max 10MB)"})
			return
		}
	}

	for _, file := range files {
		ext := filepath.Ext(file.Filename)
		path := "storage/" + uuid.New().String() + ext
		if err := c.SaveUploadedFile(file, path); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		mimeType := mime.TypeByExtension(ext)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		// Get file size
		fileInfo, err := os.Stat(path)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		sizeBytes := fileInfo.Size()

		res, err := DB.Exec("INSERT INTO media (path, mime, size_bytes, tags, processing_status) VALUES (?, ?, ?, ?, ?)", path, mimeType, sizeBytes, tags, "processing")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		id, err := res.LastInsertId()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Insert into FTS
		_, err = DB.Exec("INSERT INTO media_fts (rowid, ocr_text, tags, path) VALUES (?, ?, ?, ?)", id, "", tags, path)
		if err != nil {
			log.Printf("Failed to insert FTS: %v", err)
		}

		// Queue media processing for background execution
		jobQueue <- ProcessingJob{
			ID:       id,
			Path:     path,
			MimeType: mimeType,
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "uploaded"})
}

type Media struct {
	ID               int64
	Path             string
	Thumb            sql.NullString
	Mime             string
	Width            sql.NullInt32
	Height           sql.NullInt32
	SizeBytes        int
	Tags             string
	OcrText          sql.NullString
	ProcessingStatus string
	CreatedAt        string
}

func indexHandler(c *gin.Context) {
	media, err := getMedia("")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.HTML(http.StatusOK, "index.html", media)
}

func searchHandler(c *gin.Context) {
	q := c.Query("q")
	log.Printf("Search query: '%s'", q)
	media, err := getMedia(q)
	if err != nil {
		log.Printf("Search error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("Found %d results for query '%s'", len(media), q)
	c.HTML(http.StatusOK, "grid", media)
}

func getMedia(query string) ([]Media, error) {
	var rows *sql.Rows
	var err error
	if query == "" {
		rows, err = DB.Query("SELECT id, path, thumb, mime, width, height, size_bytes, tags, ocr_text, processing_status, created_at FROM media ORDER BY created_at DESC")
	} else {
		log.Printf("FTS query: %s", query)
		ftsRows, err := DB.Query("SELECT rowid FROM media_fts WHERE media_fts MATCH ?", query)
		if err != nil {
			log.Printf("FTS query error: %v", err)
			return nil, err
		}
		var ids []int64
		for ftsRows.Next() {
			var id int64
			ftsRows.Scan(&id)
			ids = append(ids, id)
		}
		ftsRows.Close()
		log.Printf("Matching ids: %v", ids)
		if len(ids) == 0 {
			return []Media{}, nil
		}
		placeholders := strings.Repeat("?,", len(ids))
		placeholders = placeholders[:len(placeholders)-1] // remove last comma
		rows, err = DB.Query(`
			SELECT id, path, thumb, mime, width, height, size_bytes, tags, ocr_text, processing_status, created_at
			FROM media
			WHERE id IN (`+placeholders+`)
			ORDER BY created_at DESC
		`, idsToInterface(ids)...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var media []Media
	for rows.Next() {
		var m Media
		err := rows.Scan(&m.ID, &m.Path, &m.Thumb, &m.Mime, &m.Width, &m.Height, &m.SizeBytes, &m.Tags, &m.OcrText, &m.ProcessingStatus, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		media = append(media, m)
	}
	log.Printf("Returning %d media items", len(media))
	return media, nil
}

func idsToInterface(ids []int64) []interface{} {
	result := make([]interface{}, len(ids))
	for i, id := range ids {
		result[i] = id
	}
	return result
}

func updateTagsHandler(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return
	}
	tags := c.PostForm("tags")
	log.Printf("Updating tags for id: %d to: %s", id, tags)
	_, err = DB.Exec("UPDATE media SET tags = ? WHERE id = ?", tags, id)
	if err != nil {
		log.Printf("Failed to update media tags: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Update FTS
	_, err = DB.Exec("UPDATE media_fts SET tags = ? WHERE rowid = ?", tags, id)
	if err != nil {
		log.Printf("Failed to update FTS tags for %d: %v", id, err)
	}
	c.String(http.StatusOK, tags)
}

func getMediaStatusHandler(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return
	}

	var media Media
	err = DB.QueryRow("SELECT id, path, thumb, mime, width, height, size_bytes, tags, ocr_text, processing_status, created_at FROM media WHERE id = ?", id).Scan(
		&media.ID, &media.Path, &media.Thumb, &media.Mime, &media.Width, &media.Height, &media.SizeBytes, &media.Tags, &media.OcrText, &media.ProcessingStatus, &media.CreatedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Media not found"})
		return
	}

	// Check if this is an HTMX request
	if c.GetHeader("HX-Request") == "true" {
		// Return HTML fragment for HTMX
		c.HTML(http.StatusOK, "grid", []Media{media})
	} else {
		// Return JSON for API calls
		c.JSON(http.StatusOK, gin.H{
			"id":                media.ID,
			"processing_status": media.ProcessingStatus,
			"thumb_url":         media.Thumb.String,
			"ocr_text":          media.OcrText.String,
		})
	}
}

func processMedia(id int64, path, mimeType string) {
	log.Printf("Starting media processing for ID %d, path: %s, type: %s", id, path, mimeType)
	defer log.Printf("Completed media processing for ID %d", id)

	if strings.HasPrefix(mimeType, "image/") {
		processImage(id, path)
	} else if strings.HasPrefix(mimeType, "video/") || mimeType == "image/gif" {
		processVideoOrGif(id, path)
	} else {
		log.Printf("Unsupported media type for processing: %s", mimeType)
	}
}

func processImage(id int64, path string) {
	src, err := imaging.Open(path)
	if err != nil {
		log.Printf("Failed to open image %s: %v", path, err)
		return
	}

	// Generate thumbnail
	thumb := imaging.Resize(src, 200, 0, imaging.Lanczos)
	thumbPath := "static/" + strconv.FormatInt(id, 10) + "_thumb.jpg"
	err = imaging.Save(thumb, thumbPath)
	if err != nil {
		log.Printf("Failed to save thumbnail %s: %v", thumbPath, err)
		return
	}

	// Get dimensions
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// OCR text extraction
	ocrText := extractOCR(path)

	// Update DB
	_, err = DB.Exec("UPDATE media SET thumb = ?, width = ?, height = ?, ocr_text = ?, processing_status = 'completed' WHERE id = ?", thumbPath, width, height, ocrText, id)
	if err != nil {
		log.Printf("Failed to update DB for %s: %v", id, err)
	}
	// Update FTS
	_, err = DB.Exec("UPDATE media_fts SET ocr_text = ? WHERE id = ?", ocrText, id)
	if err != nil {
		log.Printf("Failed to update FTS for %s: %v", id, err)
	}
}

func extractOCR(imagePath string) string {
	cmd := exec.Command("tesseract", imagePath, "stdout", "-l", "eng")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		log.Printf("OCR failed for %s: %v", imagePath, err)
		return ""
	}
	return strings.TrimSpace(out.String())
}

func processVideoOrGif(id int64, path string) {
	// Extract first frame
	framePath := "static/" + strconv.FormatInt(id, 10) + "_frame.jpg"
	cmd := exec.Command("ffmpeg", "-i", path, "-vframes", "1", "-q:v", "2", framePath)
	err := cmd.Run()
	if err != nil {
		log.Printf("Failed to extract frame for %s: %v", path, err)
		return
	}

	// Process as image
	processImage(id, framePath)

	// Clean up frame file
	os.Remove(framePath)
}
