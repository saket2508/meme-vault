package main

import (
	"bytes"
	"database/sql"
	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

	log.Println("listening on http://localhost:8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal(err)
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

	for _, file := range files {
		id := uuid.New().String()
		ext := filepath.Ext(file.Filename)
		path := "storage/" + id + ext
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

		_, err = DB.Exec("INSERT INTO media (id, path, mime, size_bytes, tags) VALUES (?, ?, ?, ?, ?)", id, path, mimeType, sizeBytes, tags)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Start background processing
		go processMedia(id, path, mimeType)
	}

	c.JSON(http.StatusOK, gin.H{"message": "uploaded"})
}

type Media struct {
	ID        string
	Path      string
	Thumb     string
	Mime      string
	Width     int
	Height    int
	SizeBytes int
	Tags      string
	OcrText   string
	CreatedAt string
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
	media, err := getMedia(q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.HTML(http.StatusOK, "grid.html", media)
}

func getMedia(query string) ([]Media, error) {
	var rows *sql.Rows
	var err error
	if query == "" {
		rows, err = DB.Query("SELECT id, path, thumb, mime, width, height, size_bytes, tags, ocr_text, created_at FROM media ORDER BY created_at DESC")
	} else {
		rows, err = DB.Query(`
			SELECT m.id, m.path, m.thumb, m.mime, m.width, m.height, m.size_bytes, m.tags, m.ocr_text, m.created_at
			FROM media m
			WHERE m.id IN (SELECT rowid FROM media_fts WHERE media_fts MATCH ?)
			ORDER BY m.created_at DESC
		`, query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var media []Media
	for rows.Next() {
		var m Media
		err := rows.Scan(&m.ID, &m.Path, &m.Thumb, &m.Mime, &m.Width, &m.Height, &m.SizeBytes, &m.Tags, &m.OcrText, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		media = append(media, m)
	}
	return media, nil
}

func updateTagsHandler(c *gin.Context) {
	id := c.Param("id")
	tags := c.PostForm("tags")
	_, err := DB.Exec("UPDATE media SET tags = ? WHERE id = ?", tags, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.String(http.StatusOK, tags)
}

func processMedia(id, path, mimeType string) {
	if strings.HasPrefix(mimeType, "image/") {
		processImage(id, path)
	} else if strings.HasPrefix(mimeType, "video/") || mimeType == "image/gif" {
		processVideoOrGif(id, path)
	}
}

func processImage(id, path string) {
	src, err := imaging.Open(path)
	if err != nil {
		log.Printf("Failed to open image %s: %v", path, err)
		return
	}

	// Generate thumbnail
	thumb := imaging.Resize(src, 200, 0, imaging.Lanczos)
	thumbPath := "static/" + id + "_thumb.jpg"
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
	_, err = DB.Exec("UPDATE media SET thumb = ?, width = ?, height = ?, ocr_text = ? WHERE id = ?", thumbPath, width, height, ocrText, id)
	if err != nil {
		log.Printf("Failed to update DB for %s: %v", id, err)
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

func processVideoOrGif(id, path string) {
	// Extract first frame
	framePath := "static/" + id + "_frame.jpg"
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
