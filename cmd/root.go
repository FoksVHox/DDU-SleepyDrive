package cmd

import (
	"crypto/tls"
	"errors"
	"fmt"
	"gocv.io/x/gocv"
	"image"
	"image/color"
	log2 "log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/NYTimes/logrotate"
	"github.com/apex/log"
	"github.com/apex/log/handlers/multi"
	"github.com/mitchellh/colorstring"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/FoksVHox/SleepyDrive/config"
	"github.com/FoksVHox/SleepyDrive/loggers/cli"
	"github.com/FoksVHox/SleepyDrive/router"
)

var (
	configPath = config.DefaultLocation
	debug      = false
)

var rootCommand = &cobra.Command{
	Use:   "wings",
	Short: "Runs the API server allowing programmatic control of game servers for Pterodactyl Panel.",
	PreRun: func(cmd *cobra.Command, args []string) {
		initConfig()
		initLogging()
		if tls, _ := cmd.Flags().GetBool("auto-tls"); tls {
			if host, _ := cmd.Flags().GetString("tls-hostname"); host == "" {
				fmt.Println("A TLS hostname must be provided when running wings with automatic TLS, e.g.:\n\n    ./wings --auto-tls --tls-hostname my.example.com")
				os.Exit(1)
			}
		}
	},
	Run: rootCmdRun,
}

var versionCommand = &cobra.Command{
	Use:   "version",
	Short: "Prints the current executable version and exits.",
	Run: func(cmd *cobra.Command, _ []string) {
		fmt.Printf("sleepydrive v%s\nCopyright © 2022 - %d Jimmmi Hansen & Contributors\n", "v1", time.Now().Year())
	},
}

func Execute() {
	if err := rootCommand.Execute(); err != nil {
		log2.Fatalf("failed to execute command: %s", err)
	}
}

func init() {
	rootCommand.PersistentFlags().StringVar(&configPath, "config", config.DefaultLocation, "set the location for the configuration file")
	rootCommand.PersistentFlags().BoolVar(&debug, "debug", false, "pass in order to run sleepydrive in debug mode")

	// Flags specifically used when running the API.
	rootCommand.Flags().Bool("pprof", false, "if the pprof profiler should be enabled. The profiler will bind to localhost:6060 by default")
	rootCommand.Flags().Int("pprof-block-rate", 0, "enables block profile support, may have performance impacts")
	rootCommand.Flags().Int("pprof-port", 6060, "If provided with --pprof, the port it will run on")
	rootCommand.Flags().Bool("auto-tls", false, "pass in order to have sleepydrive generate and manage it's own SSL certificates using Let's Encrypt")
	rootCommand.Flags().String("tls-hostname", "", "required with --auto-tls, the FQDN for the generated SSL certificate")
	rootCommand.Flags().Bool("ignore-certificate-errors", false, "ignore certificate verification errors when executing API calls")

	rootCommand.AddCommand(versionCommand)
}

func rootCmdRun(cmd *cobra.Command, _ []string) {
	printLogo()
	log.Debug("running in debug mode")
	log.WithField("config_file", configPath).Info("loading configuration from file")

	if ok, _ := cmd.Flags().GetBool("ignore-certificate-errors"); ok {
		log.Warn("running with --ignore-certificate-errors: TLS certificate host chains and name will not be verified")
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	log.WithField("timezone", config.Get().System.Timezone).Info("configured sleepydrive with system timezone")

	log.WithField("username", config.Get().System.User).Info("checking for pterodactyl system user")

	log.WithFields(log.Fields{
		"username": config.Get().System.Username,
		"uid":      config.Get().System.User.Uid,
		"gid":      config.Get().System.User.Gid,
	}).Info("configured system user successfully")
	if err := config.EnableLogRotation(); err != nil {
		log.WithField("error", err).Fatal("failed to configure log rotation on the system")
		return
	}

	if err := config.WriteToDisk(config.Get()); err != nil {
		log.WithField("error", err).Fatal("failed to write configuration to disk")
	}

	sys := config.Get().System
	// Ensure the archive directory exists.
	if err := os.MkdirAll(sys.ArchiveDirectory, 0o755); err != nil {
		log.WithField("error", err).Error("failed to create archive directory")
	}

	// Ensure the backup directory exists.
	if err := os.MkdirAll(sys.BackupDirectory, 0o755); err != nil {
		log.WithField("error", err).Error("failed to create backup directory")
	}

	autotls, _ := cmd.Flags().GetBool("auto-tls")
	tlshostname, _ := cmd.Flags().GetString("tls-hostname")
	if autotls && tlshostname == "" {
		autotls = false
	}

	api := config.Get().Api
	log.WithFields(log.Fields{
		"use_ssl":      api.Ssl.Enabled,
		"use_auto_tls": autotls,
		"host_address": api.Host,
		"host_port":    api.Port,
	}).Info("configuring internal webserver")

	// Create a new HTTP server instance to handle inbound requests from the Panel
	// and external clients.
	s := &http.Server{
		Addr:      api.Host + ":" + strconv.Itoa(api.Port),
		Handler:   router.Configure(),
		TLSConfig: config.DefaultTLSConfig,
	}

	profile, _ := cmd.Flags().GetBool("pprof")
	if profile {
		if r, _ := cmd.Flags().GetInt("pprof-block-rate"); r > 0 {
			runtime.SetBlockProfileRate(r)
		}
		// Catch at least 1% of mutex contention issues.
		runtime.SetMutexProfileFraction(100)

		profilePort, _ := cmd.Flags().GetInt("pprof-port")
		go func() {
			http.ListenAndServe(fmt.Sprintf("localhost:%d", profilePort), nil)
		}()
	}

	// Check if the server should run with TLS but using autocert.
	if autotls {
		m := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(path.Join(sys.RootDirectory, "/.tls-cache")),
			HostPolicy: autocert.HostWhitelist(tlshostname),
		}

		log.WithField("hostname", tlshostname).Info("webserver is now listening with auto-TLS enabled; certificates will be automatically generated by Let's Encrypt")

		// Hook autocert into the main http server.
		s.TLSConfig.GetCertificate = m.GetCertificate
		s.TLSConfig.NextProtos = append(s.TLSConfig.NextProtos, acme.ALPNProto) // enable tls-alpn ACME challenges

		// Start the autocert server.
		go func() {
			if err := http.ListenAndServe(":http", m.HTTPHandler(nil)); err != nil {
				log.WithError(err).Error("failed to serve autocert http server")
			}
		}()
		// Start the main http server with TLS using autocert.
		if err := s.ListenAndServeTLS("", ""); err != nil {
			log.WithFields(log.Fields{"auto_tls": true, "tls_hostname": tlshostname, "error": err}).Fatal("failed to configure HTTP server using auto-tls")
		}
		return
	}

	go func() {

		xmlFile := "test.xml"

		// open webcam
		webcam, err := gocv.VideoCaptureDevice(0)
		if err != nil {
			log.WithFields(log.Fields{"subsystem": "capture", "error": err}).Error("failed to open capture device")
			return
		}
		defer func(webcam *gocv.VideoCapture) {
			err := webcam.Close()
			if err != nil {
				log.WithFields(log.Fields{"subsystem": "capture", "error": err}).Error("failed to close capture device")
			}
		}(webcam)
		fmt.Println("here 1")
		// open display window
		window := gocv.NewWindow("Face Detect")
		defer func(window *gocv.Window) {
			err := window.Close()
			if err != nil {
				log.WithFields(log.Fields{"subsystem": "capture-window", "error": err}).Error("failed to close window")
			}
		}(window)
		fmt.Println("here 2")
		// prepare image matrix
		img := gocv.NewMat()
		defer img.Close()
		fmt.Println("here 3")

		// color for the rect when faces detected
		blue := color.RGBA{B: 255}
		fmt.Println("here 4")
		// load classifier to recognize faces
		classifier := gocv.NewCascadeClassifier()
		defer func(classifier *gocv.CascadeClassifier) {
			err := classifier.Close()
			if err != nil {
				log.WithFields(log.Fields{"subsystem": "classifier", "error": err}).Error("failed to close classifier")
			}
		}(&classifier)

		fmt.Println("here 5")

		if !classifier.Load(xmlFile) {
			log.WithFields(log.Fields{"subsystem": "classifier", "file": xmlFile}).Error(fmt.Sprintf("failed reading cascade file: %v", xmlFile))
		}

		fmt.Println("here 6")

		log.Debug(fmt.Sprintf("start reading camera device %v", 0))

		fmt.Println("here 7")

		for {
			if ok := webcam.Read(&img); !ok {
				log.WithFields(log.Fields{"subsystem": "camera", "device": 0}).Error("cannot read device")
			}
			if img.Empty() {
				continue
			}

			// detect faces
			rects := classifier.DetectMultiScale(img)
			log.Debug(fmt.Sprintf("found %d faces", len(rects)))

			// draw a rectangle around each face on the original image,
			// along with text identifying as "Human"
			for _, r := range rects {
				gocv.Rectangle(&img, r, blue, 3)

				size := gocv.GetTextSize("Human", gocv.FontHersheyPlain, 1.2, 2)
				pt := image.Pt(r.Min.X+(r.Min.X/2)-(size.X/2), r.Min.Y-2)
				gocv.PutText(&img, "Human", pt, gocv.FontHersheyPlain, 1.2, blue, 2)
			}

			// show the image in the window, and wait 1 millisecond
			window.IMShow(img)
			if window.WaitKey(1) >= 0 {
				break
			}
		}
	}()

	// Check if main http server should run with TLS. Otherwise reset the TLS
	// config on the server and then serve it over normal HTTP.
	if api.Ssl.Enabled {
		if err := s.ListenAndServeTLS(api.Ssl.CertificateFile, api.Ssl.KeyFile); err != nil {
			log.WithFields(log.Fields{"auto_tls": false, "error": err}).Fatal("failed to configure HTTPS server")
		}
		return
	}
	s.TLSConfig = nil
	if err := s.ListenAndServe(); err != nil {
		log.WithField("error", err).Fatal("failed to configure HTTP server")
	}

}

// Reads the configuration from the disk and then sets up the global singleton
// with all the configuration values.
func initConfig() {
	if !strings.HasPrefix(configPath, "/") {
		d, err := os.Getwd()
		if err != nil {
			log2.Fatalf("cmd/root: could not determine directory: %s", err)
		}
		configPath = path.Clean(path.Join(d, configPath))
	}
	err := config.FromFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			exitWithConfigurationNotice()
		}
		log2.Fatalf("cmd/root: error while reading configuration file: %s", err)
	}
	if debug && !config.Get().Debug {
		config.SetDebugViaFlag(debug)
	}
}

// Configures the global logger for Zap so that we can call it from any location
// in the code without having to pass around a logger instance.
func initLogging() {
	dir := config.Get().System.LogDirectory
	if err := os.MkdirAll(path.Join(dir, "/install"), 0o700); err != nil {
		log2.Fatalf("cmd/root: failed to create install directory path: %s", err)
	}
	p := filepath.Join(dir, "/sleepydrive.log")
	w, err := logrotate.NewFile(p)
	if err != nil {
		log2.Fatalf("cmd/root: failed to create sleepydrive log: %s", err)
	}
	log.SetLevel(log.InfoLevel)
	if config.Get().Debug {
		log.SetLevel(log.DebugLevel)
	}
	log.SetHandler(multi.New(cli.Default, cli.New(w.File, false)))
	log.WithField("path", p).Info("writing log files to disk")
}

// Prints the wings logo, nothing special here!
func printLogo() {
	fmt.Printf(colorstring.Color(`
                     ____
__ [blue][bold]Digital Design & Udvikling[reset] _____/___/_______ _______ ______
\_____\    \/\/    /   /       /  __   /   ___/
   \___\          /   /   /   /  /_/  /___   /
        \___/\___/___/___/___/___    /______/
                            /_______/ [bold]%s[reset]
Copyright © 2022 - %d Jimmi Hansen & Contributors
This software is made available under the terms of the MIT license.
The above copyright notice and this permission notice shall be included
in all copies or substantial portions of the Software.%s`), "V1", time.Now().Year(), "\n\n")
}

func exitWithConfigurationNotice() {
	fmt.Print(colorstring.Color(`
[_red_][white][bold]Error: Configuration File Not Found[reset]
sleepydrive was not able to locate your configuration file, and therefore is not
able to complete its boot process. Please ensure you have copied your instance
configuration file into the default location below.
Default Location: /etc/ddu/config.yml
[yellow]This is not a bug with this software. Please do not make a bug report
for this issue, it will be closed.[reset]
`))
	os.Exit(1)
}
