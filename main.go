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
	Name       string `json:"name"`
	Title      string `json:"title"`
	Pattern    string `json:"pattern"`
	Fps        []int  `json:"fps"`
	SavePath   string `json:"savePath"`
	Resolution string `json:"resolution"`
}

type CaptureImageList []CaptureImage

type CaptureImageState struct {
	LastProcessedImage string `json:"lastProcessedImage"`
}

type CaptureImageStateMap map[string]CaptureImageState

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

func loadState(filePath string) (CaptureImageStateMap, error) {
	state := make(CaptureImageStateMap)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return nil, err
	}
	err = json.Unmarshal(data, &state)
	return state, err
}

func saveState(filePath string, state CaptureImageStateMap) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0644)
}

func generateVideosWithLock(captureImages CaptureImageList) {
	generateVideosMutex.Lock()
	defer generateVideosMutex.Unlock()

	generateVideos(captureImages)
}

func generateVideos(captureImages CaptureImageList) {
	stateFilePath := "/data/state.json"
	for _, captureImage := range captureImages {
		log.Printf("[INFO] Capture image: %+v", captureImage)
		generateVideo(captureImage, stateFilePath)
	}
}

func setupLog(dbg bool) {
	logOpts := []lgr.Option{lgr.Msec, lgr.LevelBraces, lgr.StackTraceOnError}
	if dbg {
		logOpts = []lgr.Option{lgr.Debug, lgr.CallerFile, lgr.CallerFunc, lgr.Msec, lgr.LevelBraces, lgr.StackTraceOnError}
	}
	lgr.SetupStdLogger(logOpts...)
}

func generateVideo(captureImage CaptureImage, stateFilePath string) {
	log.Printf("[INFO] Generate video for: %+v", captureImage)

	state, err := loadState(stateFilePath)
	if err != nil {
		log.Printf("[ERROR] failed to load state: %v", err)
		return
	}

	matches, err := filepath.Glob(captureImage.Pattern)
	if err != nil {
		log.Printf("[ERROR] failed to find files: %v from pattern: %s", err, captureImage.Pattern)
		return
	}
	if len(matches) == 0 {
		log.Printf("[INFO] no files found for pattern: %s", captureImage.Pattern)
		return
	}

	lastProcessedImage := state[captureImage.Name].LastProcessedImage
	newImages := filterNewImages(matches, lastProcessedImage)
	if len(newImages) == 0 {
		log.Printf("[INFO] no new images found for pattern: %s", captureImage.Pattern)
		return
	}

	inputListFile, err := os.CreateTemp("", "input_list_*.txt")
	if err != nil {
		log.Fatalf("[ERROR] failed to create temporary file: %v", err)
	}
	defer os.Remove(inputListFile.Name())

	for _, fps := range captureImage.Fps {
		// clean inputListFile content
		_ = inputListFile.Truncate(0)

		currentFps := strconv.Itoa(fps)

		finalFileName := captureImage.SavePath + "/" + captureImage.Name + "_" + currentFps + "_fps.mp4"
		tempFileName := captureImage.SavePath + "/" + captureImage.Name + "_temp_" + currentFps + "_fps.mp4"
		newSegmentFileName := captureImage.SavePath + "/" + captureImage.Name + "_new_" + currentFps + "_fps.mp4"

		for _, img := range newImages {
			absPath, err := filepath.Abs(img)
			if err != nil {
				log.Fatalf("[ERROR] failed to get absolute path: %v", err)
			}
			_, err = inputListFile.WriteString("file '" + absPath + "'\n")
			if err != nil {
				log.Fatalf("[ERROR] failed to write to temporary file: %v", err)
			}
		}

		// Write new segment to file
		log.Printf("[INFO] Creating new video segment: %s fron list: %s", newSegmentFileName,
			inputListFile.Name())

		err = ffmpeg.Input(
			inputListFile.Name(),
			ffmpeg.KwArgs{
				"f":             "concat",
				"safe":          "0",
				"r":             fps,
				"reinit_filter": "0",
			}).
			Output(newSegmentFileName, ffmpeg.KwArgs{
				"vf": fmt.Sprintf(
					"scale=%v:force_original_aspect_ratio=decrease:eval=frame,pad=%v:-1:-1:eval=frame",
					captureImage.Resolution, captureImage.Resolution),
				"c:v": "libx264",
			}).
			OverWriteOutput().ErrorToStdOut().Run()
		if err != nil {
			log.Fatalf("[ERROR] failed to create new video segment: %v", err)
		}

		// Concatenate videos
		err = concatenateVideos(finalFileName, newSegmentFileName, tempFileName, fps)
		if err != nil {
			log.Printf("[ERROR] failed to concatenate videos: %v", err)
			return
		}

		log.Printf("[INFO] Renaming file: %s to: %s", tempFileName, finalFileName)
		err = os.Rename(tempFileName, finalFileName)
		if err != nil {
			log.Printf("[ERROR] failed to rename file: %v", err)
			return
		}

		// Remove if new segment file exists
		log.Printf("[INFO] Removing new segment file: %s", newSegmentFileName)
		if _, err := os.Stat(newSegmentFileName); err == nil {
			err = os.Remove(newSegmentFileName)
			if err != nil {
				log.Printf("[ERROR] failed to remove new segment file: %v", err)
				return
			}
		}

		state[captureImage.Name] = CaptureImageState{LastProcessedImage: newImages[len(newImages)-1]}
		err = saveState(stateFilePath, state)
		if err != nil {
			log.Printf("[ERROR] failed to save state: %v", err)
			return
		}
	}

	inputListFile.Close()
}

func filterNewImages(images []string, lastProcessedImage string) []string {
	if lastProcessedImage == "" {
		return images
	}
	for i, img := range images {
		if img == lastProcessedImage {
			return images[i+1:]
		}
	}
	return images
}

func concatenateVideos(existingVideo, newSegment, output string, fps int) error {
	// Check if the existing video file exists
	if _, err := os.Stat(existingVideo); os.IsNotExist(err) {
		// If the existing video does not exist, treat the new segment as the first segment
		err := os.Rename(newSegment, output)
		if err != nil {
			return err
		}

		return nil
	} else if err != nil {
		return fmt.Errorf("failed to stat existing video: %w", err)
	}

	// Create a temporary file to store the list of input files
	inputListFile, err := os.CreateTemp("", "concat_list_*.txt")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer os.Remove(inputListFile.Name())

	// Write the absolute paths of the videos to the temporary file
	existingVideoAbs, err := filepath.Abs(existingVideo)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for existing video: %w", err)
	}
	newSegmentAbs, err := filepath.Abs(newSegment)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for new segment: %w", err)
	}

	_, err = inputListFile.WriteString("file '" + existingVideoAbs + "'\n")
	if err != nil {
		return fmt.Errorf("failed to write to temporary file: %w", err)
	}
	if existingVideo != newSegment {
		_, err = inputListFile.WriteString("file '" + newSegmentAbs + "'\n")
		if err != nil {
			return fmt.Errorf("failed to write to temporary file: %w", err)
		}
	}
	inputListFile.Close()

	log.Printf("[INFO] Concatenating videos: %s, %s", existingVideo, newSegment)

	// Use ffmpeg to concatenate the videos listed in the temporary file
	return ffmpeg.Input(inputListFile.Name(), ffmpeg.KwArgs{"f": "concat", "safe": "0"}).
		Output(output, ffmpeg.KwArgs{"c:v": "libx264"}).
		OverWriteOutput().ErrorToStdOut().Run()
}

func isValidFilename(filename string) bool {
	re := regexp.MustCompile(`^[a-zA-Z0-9_\-.]+$`)
	return re.MatchString(filename)
}
