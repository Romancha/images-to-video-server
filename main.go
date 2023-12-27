package main

import (
	"encoding/json"
	"fmt"
	vidio "github.com/AlexEidt/Vidio"
	"github.com/gin-gonic/gin"
	"github.com/go-pkgz/lgr"
	"github.com/jessevdk/go-flags"
	"github.com/robfig/cron/v3"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var opts struct {
	ConfigPath string `long:"config-path" env:"CONFIG_PATH" description:"Config path" default:"./data/config.json"`
	CronSpec   string `long:"cron-spec" env:"CRON_SPEC" description:"Cron spec" default:"0 */30 * * * *"`

	TemplatesPath string `long:"templates-path" env:"TEMPLATES_PATH" description:"Templates path" default:"./templates"`
	Port          int    `long:"port" env:"PORT" description:"Port" default:"8080"`

	Debug bool `long:"debug" env:"DEBUG" description:"debug mode"`
}

type CaptureImage struct {
	Name     string `json:"name"`
	Pattern  string `json:"pattern"`
	Fps      int    `json:"fps"`
	SavePath string `json:"savePath"`
}

type CaptureImageList []CaptureImage

func main() {
	fmt.Println("Video server started")
	if _, err := flags.Parse(&opts); err != nil {
		log.Printf("[ERROR] failed to parse flags: %v", err)
		os.Exit(1)
	}

	setupLog(opts.Debug)

	log.Printf("[INFO] opts: %+v", opts)

	config, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		log.Fatalf("[ERROR] failed to read config: %v", err)
	}

	var captureImages CaptureImageList
	err = json.Unmarshal(config, &captureImages)
	if err != nil {
		log.Fatalf("[ERROR] failed to parse config: %v", err)
	}

	log.Printf("[INFO] Capture Images config: %+v", captureImages)

	videoCron := cron.New(cron.WithSeconds())
	_, err = videoCron.AddFunc(opts.CronSpec, func() {
		for _, captureImage := range captureImages {
			log.Printf("[INFO] Capture image: %+v", captureImage)
			generateVideo(captureImage)
		}
	})
	if err != nil {
		log.Fatalf("[ERROR] failed to add cron: %v", err)
	}
	videoCron.Start()

	router := gin.Default()
	errSetTrusted := router.SetTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8"})
	if errSetTrusted != nil {
		log.Fatalf("[ERROR] failed to set trusted proxy")
	}

	router.LoadHTMLGlob("./templates/*")

	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "videos.html", gin.H{
			"captureImages": captureImages,
		})
	})

	router.GET("/stream/:filename", func(c *gin.Context) {
		filename := c.Param("filename")
		var captureImage CaptureImage
		for _, ci := range captureImages {
			nameWithoutExtension := filename[:len(filename)-len(filepath.Ext(filename))]
			if ci.Name == nameWithoutExtension {
				captureImage = ci
				break
			}
		}
		if captureImage.Name == "" {
			c.String(http.StatusNotFound, "Video not found.")
			return
		}

		videoPath := captureImage.SavePath + "/" + filename

		file, err := os.Open(videoPath)
		if err != nil {
			c.String(http.StatusNotFound, "Video not found.")
			return
		}
		defer file.Close()

		fileInfo, err := file.Stat()
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to get file info.")
			return
		}

		fileSize := fileInfo.Size()
		startByte := int64(0)
		endByte := fileSize - 1

		rangeHeader := c.GetHeader("Range")
		if rangeHeader != "" {
			match := regexp.MustCompile(`bytes=(\d+)-(\d*)`).FindStringSubmatch(rangeHeader)
			if len(match) != 0 {
				startByte, _ = strconv.ParseInt(match[1], 10, 64)
				if match[2] != "" {
					endByte, _ = strconv.ParseInt(match[2], 10, 64)
				}
			}
		}

		if startByte > endByte {
			c.String(http.StatusRequestedRangeNotSatisfiable, "Invalid byte range.")
			return
		}

		contentRange := fmt.Sprintf("bytes %d-%d/%d", startByte, endByte, fileSize)
		contentLength := fmt.Sprintf("%d", endByte-startByte+1)

		log.Printf("[DEBUG] request filename: %s "+
			"\nrangeHeader: %s \nfileSize: %d \nstartByte: %d \nendByte: %d \ncontentRange: %s "+
			"\ncontentLength: %s", filename, rangeHeader, fileSize, startByte, endByte, contentRange, contentLength)

		c.Header("Content-Type", "video/mp4")
		c.Header("Accept-Ranges", "bytes")
		c.Header("Content-Range", contentRange)
		c.Header("Content-Length", contentLength)

		c.Status(http.StatusPartialContent)

		file.Seek(startByte, 0)
		buffer := make([]byte, endByte-startByte+1)
		file.Read(buffer)
		c.Writer.Write(buffer)

		log.Printf("[DEBUG] response file: %s", filename)
	})

	err = router.Run(fmt.Sprintf(":%d", opts.Port))
	if err != nil {
		log.Fatalf("[ERROR] failed to run router: %v", err)
	}

}

func setupLog(dbg bool) {
	logOpts := []lgr.Option{lgr.Msec, lgr.LevelBraces, lgr.StackTraceOnError}
	if dbg {
		logOpts = []lgr.Option{lgr.Debug, lgr.CallerFile, lgr.CallerFunc, lgr.Msec, lgr.LevelBraces, lgr.StackTraceOnError}
	}
	lgr.SetupStdLogger(logOpts...)
}

func generateVideo(captureImage CaptureImage) {
	log.Printf("[INFO] Generate video for: %+v", captureImage)

	matches, err := filepath.Glob(captureImage.Pattern)
	if err != nil {
		log.Printf("[ERROR] failed to find files: %v from pattern: %s", err, captureImage.Pattern)
	}
	if len(matches) == 0 {
		log.Printf("[INFO] no files found for pattern: %s", captureImage.Pattern)
		return
	}

	w, h, _, err := vidio.Read(matches[0])
	if err != nil {
		log.Printf("[ERROR] failed to read image: %v", err)
	}

	options := vidio.Options{FPS: float64(captureImage.Fps)}

	tempFileName := captureImage.SavePath + "/" + captureImage.Name + "_temp_.mp4"
	video, err := vidio.NewVideoWriter(tempFileName, w, h, &options)
	if err != nil {
		log.Fatalf("[ERROR] failed to create video writer: %v", err)
	}

	defer video.Close()

	log.Printf("[DEBUG] matches: %+v", matches)

	for _, name := range matches {
		log.Printf("[DEBUG] Read image: %s", name)

		_, _, img, _ := vidio.Read(name)
		if err != nil {
			log.Fatalf("[ERROR] failed to read image: %v", err)
		}

		errWrite := video.Write(img)
		if errWrite != nil {
			log.Fatalf("[ERROR] failed to write image: %v", errWrite)
		}
	}

	originalName := strings.Replace(tempFileName, "_temp_", "", 1)
	err = os.Rename(tempFileName, originalName)
}
