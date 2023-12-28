# Webcam Timelapse

This project is a Go application that creates a timelapse video from webcam capture images.

## Description

This application use ffmpeg to create a mp4 timelapse video from a couple of images and display the videos in a simple web page.

Another app https://github.com/Romancha/webcam-screenshot-capture is used to capture images from webcam.

## Usage

1. Configure config.json file with your webcam capture images folders and the output folder for the videos. See many arguments in opts struct in main.go file.
2. Run as Go binary (ffmpeg must be installed) or from Docker.
3. Open the web page localhost:8080 to see the videos.

## Contributing

Contributions are welcome. Please fork the repository and create a pull request with your changes.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.