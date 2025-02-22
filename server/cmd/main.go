package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"server/internal/server"
	"server/internal/server/clients"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const (
	dockerMontedDataDir   = "/gameserver/data"
	dockerMountedCertsDir = "/gameserver/certs"
)

type config struct {
	Port     int
	DataPath string
	CertPath string
	KeyPath  string
}

var (
	defaultConfig = &config{
		Port: 8080,
	}
	configPath = flag.String("config", ".env", "Path to the config file")
)

func loadConfig() *config {
	cfg := defaultConfig
	cfg.DataPath = os.Getenv("DATA_PATH")
	cfg.CertPath = os.Getenv("CERT_PATH")
	cfg.KeyPath = os.Getenv("KEY_PATH")

	port, err := strconv.Atoi(os.Getenv("PORT"))
	if err != nil {
		log.Printf("Errors parsing PORT, using %d", cfg.Port)
		return cfg
	}
	cfg.Port = port

	return cfg
}

func coalescePaths(fallbacks ...string) string {
	for i, path := range fallbacks {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			message := fmt.Sprintf("File/folder not found at %s", path)
			if i < len(fallbacks)-1 {
				log.Printf("%s - going to try %s", message, fallbacks[i+1])
			} else {
				log.Printf("%s - no more fallbacks to try", message)
			}
		} else {
			log.Printf("File/folder found at %s", path)
			return path
		}
	}
	return ""
}

func resolveLiveCertsPath(certPath string) string {
	normalizedPath := strings.ReplaceAll(certPath, "\\", "/")
	pathComponents := strings.Split(normalizedPath, "/live/")

	if len(pathComponents) >= 2 {
		pathTail := pathComponents[len(pathComponents)-1]

		// Try to load the certificates exactly as they appear in the config,
		// otherwise assume they are in the Docker-mounted folder for certs
		return coalescePaths(certPath, filepath.Join(dockerMountedCertsDir, "live", pathTail))
	}

	return certPath
}

func main() {
	flag.Parse()
	err := godotenv.Load(*configPath)
	cfg := defaultConfig

	if err != nil {
		log.Printf("Error loading config file, defaulting to %+v\n", defaultConfig)
	} else {
		cfg = loadConfig()
	}

	cfg.DataPath = coalescePaths(cfg.DataPath, dockerMontedDataDir, "./data", ".")

	hub := server.NewHub(cfg.DataPath)

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		hub.Serve(clients.NewWebSocketClient, w, r)
	})

	go hub.Run()
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("Starting server on %s", addr)

	cfg.CertPath = resolveLiveCertsPath(cfg.CertPath)
	cfg.KeyPath = resolveLiveCertsPath(cfg.KeyPath)

	log.Printf("Using cert at %s and key at %s", cfg.CertPath, cfg.KeyPath)
	err = http.ListenAndServeTLS(addr, cfg.CertPath, cfg.KeyPath, nil)

	if err != nil {
		log.Printf("No certificate found (%v), starting server without TLS", err)
		err = http.ListenAndServe(addr, nil)
		if err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}
}
