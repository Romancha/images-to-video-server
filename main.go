package main

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/go-pkgz/lgr"
	"github.com/jessevdk/go-flags"
	"github.com/robfig/cron/v3"
	ffmpeg "github.com/u2takey/ffmpeg-go"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var generateVideosMutex sync.Mutex

var opts struct {
	ConfigPath string `long:"config-path" env:"CONFIG_PATH" description:"Config path" default:"./data/config.json"`
	CronSpec   string `long:"cron-spec" env:"CRON_SPEC" description:"Cron spec" default:"0 */01 * * * *"`

	Port int `long:"port" env:"PORT" description:"Port" default:"8080"`

	Debug bool `long:"debug" env:"DEBUG" description:"debug mode"`
}

type CaptureImage struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Pattern  string `json:"pattern"`
	Fps      []int  `json:"fps"`
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

	// Generate video on startup
	go generateVideosWithLock(captureImages)

	// Start cron job to generate videos
	videoCron := cron.New(cron.WithSeconds())
	_, err = videoCron.AddFunc(opts.CronSpec, func() {
		generateVideosWithLock(captureImages)
	})
	if err != nil {
		log.Fatalf("[ERROR] failed to add cron: %v", err)
	}
	videoCron.Start()

	// Start web server
	router := gin.Default()
	errSetTrusted := router.SetTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8"})
	if errSetTrusted != nil {
		log.Fatalf("[ERROR] failed to set trusted proxy")
	}

	router.LoadHTMLGlob("./templates/*")
	router.StaticFS("./static", http.Dir("./static"))

	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "videos.html", gin.H{
			"captureImages": captureImages,
		})
	})

	var videoNames map[string]CaptureImage
	videoNames = make(map[string]CaptureImage)
	for _, captureImage := range captureImages {
		for _, fps := range captureImage.Fps {
			videoName := captureImage.Name + "_" + strconv.Itoa(fps) + "_fps.mp4"
			videoNames[videoName] = captureImage
		}
	}

	router.GET("/stream/:filename", func(c *gin.Context) {
		filename := c.Param("filename")
		if !isValidFilename(filename) {
			c.String(http.StatusBadRequest, "Invalid filename.")
			return
		}

		captureImage, found := videoNames[filename]
		if !found {
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
		_, err = io.CopyN(c.Writer, file, endByte-startByte+1)
		if err != nil {
			log.Printf("[ERROR] failed to copy file content: %v", err)
			c.String(http.StatusInternalServerError, "Failed to stream video.")
			return
		}

		log.Printf("[DEBUG] response file: %s", filename)
	})

	err = router.Run(fmt.Sprintf(":%d", opts.Port))
	if err != nil {
		log.Fatalf("[ERROR] failed to run router: %v", err)
	}

}

func generateVideosWithLock(captureImages CaptureImageList) {
	generateVideosMutex.Lock()
	defer generateVideosMutex.Unlock()

	generateVideos(captureImages)
}

func generateVideos(captureImages CaptureImageList) {
	for _, captureImage := range captureImages {
		log.Printf("[INFO] Capture image: %+v", captureImage)
		generateVideo(captureImage)
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
	log.Printf("[INFO] Generate videoWriter for: %+v", captureImage)

	matches, err := filepath.Glob(captureImage.Pattern)
	if err != nil {
		log.Printf("[ERROR] failed to find files: %v from pattern: %s", err, captureImage.Pattern)
	}
	if len(matches) == 0 {
		log.Printf("[INFO] no files found for pattern: %s", captureImage.Pattern)
		return
	}

	for _, fps := range captureImage.Fps {
		tempFileName := captureImage.SavePath + "/" + captureImage.Name + "_temp_" + strconv.Itoa(fps) + "_fps.mp4"

		err = ffmpeg.Input(captureImage.Pattern, ffmpeg.KwArgs{"pattern_type": "glob", "framerate": fps}).
			Output(tempFileName, ffmpeg.KwArgs{"c:v": "libx264"}).
			OverWriteOutput().ErrorToStdOut().Run()
		if err != nil {
			log.Fatalf("[ERROR] failed to create video: %v", err)
		}

		originalName := strings.Replace(tempFileName, "_temp_", "_", 1)
		log.Printf("[DEBUG] originalName: %s", originalName)

		err = os.Rename(tempFileName, originalName)
		if err != nil {
			log.Printf("[ERROR] failed to rename file: %v", err)
		}
	}
}

func isValidFilename(filename string) bool {
	re := regexp.MustCompile(`^[a-zA-Z0-9_\-.]+$`)
	return re.MatchString(filename)
}
