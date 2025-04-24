package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/net/html"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const (
	baseURL     = "https://www.tucan.tu-darmstadt.de"
	loginScript = "https://www.tucan.tu-darmstadt.de/scripts/mgrqispi.dll"
	userAgent   = "Mozilla/5.0"
)

func main() {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// Pull username and password from environment variables
	username := os.Getenv("TUCAN_USERNAME")
	password := os.Getenv("TUCAN_PASSWORD")
	if username == "" || password == "" {
		log.Fatal("Please set TUCAN_USERNAME and TUCAN_PASSWORD environment variables")
	}

	// Fetch initial iCalendar data
	mergedCalendar := fetchIcalData(username, password)

	// Save merged calendar to a file
	os.WriteFile("merged_calendar.ics", []byte(mergedCalendar), 0644)
	fmt.Println("Saved merged_calendar.ics")

	// Start the web server to serve the merged calendar and update it every hour
	runWebServer(username, password, &mergedCalendar)
}

func fetchIcalData(username, password string) string {
	var icsContents []string

	// Create a new client with a cookie jar
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Log in with the client and get the session cookie
	session := login(client, username, password)

	// Setup concurrent requests
	var wg sync.WaitGroup
	icsChan := make(chan string, 64)

	for i := -3; i < 8; i++ {
		month := time.Now().AddDate(0, i, 0).Format("2006-01")
		date := "Y" + strings.ReplaceAll(month, "-", "M")

		// Create a new goroutine for each month
		wg.Add(1)

		go func(month string) {
			form := url.Values{
				"APPNAME":   {"CampusNet"},
				"PRGNAME":   {"SCHEDULER_EXPORT_START"},
				"ARGUMENTS": {"sessionno,menuid,date"},
				"sessionno": {session},
				"menuid":    {"000272"},
				"date":      {date},
				"month":     {date},
				"week":      {"0"},
			}

			// Get the iCalendar file
			ics, err := getIcalendar(client, form)
			if err != nil {
				log.Printf("Error getting iCalendar for %s: %v", month, err)
				wg.Done()
				return
			}
			if ics == "" {
				log.Printf("No iCalendar data for %s", month)
				wg.Done()
				return
			}

			fmt.Printf("Got iCalendar for %s\n", month)

			// Send the iCalendar data to the channel
			icsChan <- ics

			wg.Done()
		}(month)
	}

	// Close the channel when all goroutines are done
	wg.Wait()
	close(icsChan)

	// Collect all iCalendar data from the channel
	for ics := range icsChan {
		icsContents = append(icsContents, ics)
	}

	// Merge all .ics files
	return mergeIcs(icsContents)
}

func runWebServer(username, password string, mergedCalendar *string) {
	// Start a goroutine to update the merged calendar every hour
	go startCalendarUpdater(username, password, mergedCalendar)

	// Serve the merged calendar
	http.HandleFunc("/calendar.ics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(*mergedCalendar))
	})
	fmt.Println("Serving iCalendar at http://localhost:8080/calendar.ics")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func startCalendarUpdater(username, password string, mergedCalendar *string) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		<-ticker.C
		fmt.Println("Updating merged calendar...")
		*mergedCalendar = fetchIcalData(username, password)
		os.WriteFile("merged_calendar.ics", []byte(*mergedCalendar), 0644)
		fmt.Println("Updated merged_calendar.ics")
	}
}

func login(client *http.Client, username, password string) string {
	form := url.Values{
		"usrname":   {username},
		"pass":      {password},
		"APPNAME":   {"CampusNet"},
		"PRGNAME":   {"LOGINCHECK"},
		"ARGUMENTS": {"clino,usrname,pass,menuno,menu_type,browser,platform"},
		"clino":     {"000000000000001"},
		"menuno":    {"000000"},
		"menu_type": {"classic"},
	}

	// Prepare the POST request
	req, err := http.NewRequest("POST", loginScript, strings.NewReader(form.Encode()))
	if err != nil {
		log.Fatalf("Failed to build login request: %v", err)
	}

	// Set headers to mimic a real browser
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	// Send the POST request
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Login request failed: %v", err)
	}

	// Check for incorrect login
	body, _ := io.ReadAll(resp.Body)
	if incorectLogin(string(body)) {
		log.Fatal("Login failed: incorrect username or password")
	}

	defer resp.Body.Close()

	// Retrieve the cookies
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == "cnsc" {
			cookie = c.Value
			break
		}
	}
	// If we don't find the session cookie, log an error
	if cookie == "" {
		log.Fatal("Login failed: no session cookie received")
	}

	log.Printf("Succesfully got cookie: %s", cookie)

	// Check for Refresh header and follow the redirect if present
	if refreshHeader := resp.Header.Get("Refresh"); refreshHeader != "" {
		// The refresh header is in the form "time;url"
		// Extract the URL from the refresh header
		parts := strings.Split(refreshHeader, ";")
		if len(parts) > 1 {
			redirectURL := strings.TrimSpace(parts[1])

			// remove leading "url="
			redirectURL = strings.TrimPrefix(redirectURL, "URL=")

			if !strings.HasPrefix(redirectURL, "http") {
				// If the URL is relative, we need to prepend the base URL
				redirectURL = baseURL + redirectURL
			}

			// Follow the redirect
			resp, err = client.Get(redirectURL)
			if err != nil {
				log.Fatalf("Failed to follow redirect: %v", err)
			}
			defer resp.Body.Close()
		}
	}

	// Now, parse the HTML body to extract the sessionId
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Error reading response body: %v", err)
	}

	sessionID := extractSessionID(string(body))
	if sessionID == "" {
		log.Fatal("No session ID found")
	}
	log.Printf("SSuccesfully got session ID: %s", sessionID)

	return sessionID
}

func getIcalendar(client *http.Client, values url.Values) (string, error) {

	req, err := http.NewRequest("POST", loginScript, strings.NewReader(values.Encode()))
	if err != nil {
		log.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error doing request: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	//log.Printf("Response for month %s: %s", date, string(body))

	if accessDenied(string(body)) {
		return "", fmt.Errorf("access denied")
	}

	if noEvents(string(body)) {
		return "", fmt.Errorf("no events")
	}

	link := extractFiletransferLink(string(body))
	if link == "" {
		return "", fmt.Errorf("no .ics link found")
	}

	// Download .ics file
	icsResp, err := client.Get(link)
	if err != nil {
		return "", fmt.Errorf("couldn't download .ics from %s: %v", link, err)
	}
	defer icsResp.Body.Close()

	icsData, _ := io.ReadAll(icsResp.Body)

	// Convert UTF-16 to UTF-8
	utf8Data := UTF16ToUTF8(icsData)
	if utf8Data == nil {
		return "", fmt.Errorf("error converting UTF-16 to UTF-8")
	}

	return string(utf8Data), nil
}

// just grabs the first .ics link it finds
func extractFiletransferLink(htmlStr string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(htmlStr))
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}
		token := tokenizer.Token()
		if token.Type == html.StartTagToken && token.Data == "a" {
			for _, attr := range token.Attr {
				if attr.Key == "href" && strings.Contains(attr.Val, "filetransfer.exe") {
					if strings.HasPrefix(attr.Val, "http") {
						return attr.Val
					}
					return baseURL + attr.Val
				}
			}
		}
	}
	return ""
}

// Function to extract the sessionId from the HTML body
func extractSessionID(body string) string {
	// Parse the HTML response to find the sessionId inside the <div id="sessionId"> element
	tokenizer := html.NewTokenizer(strings.NewReader(body))
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			break
		}

		token := tokenizer.Token()
		if token.Type == html.StartTagToken && token.Data == "div" {
			// Look for the id="sessionId" attribute
			for _, attr := range token.Attr {
				if attr.Key == "id" && attr.Val == "sessionId" {
					// Get the content inside the div tag
					tokenizer.Next() // Move to the text node
					token = tokenizer.Token()
					return token.Data
				}
			}
		}
	}
	return ""
}

// dumb concat of .ics files skipping BEGIN:VCALENDAR/END:VCALENDAR
func mergeIcs(calendars []string) string {
	var merged bytes.Buffer
	merged.WriteString("BEGIN:VCALENDAR\n")
	for _, ics := range calendars {
		lines := strings.Split(ics, "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "BEGIN:VCALENDAR") && !strings.HasPrefix(line, "END:VCALENDAR") {
				merged.WriteString(line + "\n")
			}
		}
	}
	merged.WriteString("END:VCALENDAR\n")
	return merged.String()
}

func UTF16ToUTF8(utf16 []byte) []byte {
	// Convert UTF-16 to UTF-8
	decoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()

	reader := transform.NewReader(bytes.NewReader(utf16), decoder)

	utf8, err := io.ReadAll(reader)
	if err != nil {
		log.Fatalf("Error converting UTF-16 to UTF-8: %v", err)
	}
	return utf8
}

func accessDenied(body string) bool {
	return strings.Contains(body, "<body class=\"access_denied\">")
}

func incorectLogin(body string) bool {
	return strings.Contains(body, "<p>Bitte versuchen Sie es erneut. Überprüfen Sie ggf. Ihre Zugangsdaten.</p>")
}

func noEvents(body string) bool {
	return strings.Contains(body, "<td class=\"tbdata_error\">Die Kalenderdatei konnte nicht erstellt werden, weil im gewählten Zeitraum keine Termine vorhanden sind.</td>")
}
