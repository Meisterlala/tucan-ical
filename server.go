package main

import (
	"log"
	"net/http"
	"os"
)

func runWebServer(port string) {
	// Serve the merged calendar
	http.HandleFunc("/tucan.ics", httpTucan)
	http.HandleFunc("/health", httpHealth)

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// Serve the merged calendar at /tucan.ics
func httpTucan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/calendar")
	data, err := os.ReadFile(icalFile)
	if err != nil {
		http.Error(w, "Failed to read calendar file", http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

// Health check endpoint
func httpHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
