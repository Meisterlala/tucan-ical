package main

import (
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
)

const (
	baseURL     = "https://www.tucan.tu-darmstadt.de"
	loginScript = "https://www.tucan.tu-darmstadt.de/scripts/mgrqispi.dll"
	userAgent   = "TUCaN iCalendar Extractor/1.0"

	icalFile = "merged_calendar.ics"
)

func main() {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found, proceeding without it")
	}

	// Get the server port from the environment variable, default to 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	go runWebServer(port)

	// Pull username and password from environment variables
	username := os.Getenv("TUCAN_USERNAME")
	password := os.Getenv("TUCAN_PASSWORD")
	if username == "" || password == "" {
		log.Fatal("Please set TUCAN_USERNAME and TUCAN_PASSWORD environment variables")
	}

	// Get the update interval from the environment variable, default to 2 hours
	intervalStr := os.Getenv("UPDATE_INTERVAL")
	interval := 2 * time.Hour
	if intervalStr != "" {
		parsedInterval, err := time.ParseDuration(intervalStr)
		if err != nil {
			log.Printf("Invalid UPDATE_INTERVAL format, using default 2h: %v", err)
		} else {
			interval = parsedInterval
		}
	}

	// Fetch the iCalendar data
	go startCalendarUpdater(username, password, interval)

	select {}
}
