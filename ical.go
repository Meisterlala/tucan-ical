package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func startCalendarUpdater(username, password string, update_interval time.Duration) {
	ticker := time.NewTicker(update_interval)
	icals := make(map[string]string)

	defer ticker.Stop()

	for {
		log.Println("Updating calendar...")

		// Fetch iCalendar data
		newIcals := fetchIcalData(username, password)

		// Overwrite existing iCalendar data with new data
		for month, ics := range newIcals {
			if _, exists := icals[month]; !exists {
				icals[month] = ics
			}
		}

		// Merge iCalendar data
		var calendarValues []string
		for _, ics := range icals {
			calendarValues = append(calendarValues, ics)
		}
		mergedCalendar := mergeIcs(calendarValues)

		if len(calendarValues) > 0 {
			os.WriteFile(icalFile, []byte(mergedCalendar), 0644)
			log.Println("Updated ", icalFile)
		} else {
			log.Println("No calendar data to update")
		}

		<-ticker.C
	}
}

func fetchIcalData(username, password string) map[string]string {
	icals := make(map[string]string)

	// Create a new client with a cookie jar
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Log in with the client and get the session cookie
	session, err := login(client, username, password)
	if err != nil {
		log.Printf("Login failed: %v", err)
		return icals
	}

	for i := -3; i < 8; i++ {
		month := time.Now().AddDate(0, i, 0).Format("2006-01")
		date := "Y" + strings.ReplaceAll(month, "-", "M")

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
			continue
		}
		if ics == "" {
			log.Printf("No iCalendar data for %s", month)
			continue
		}

		event_count := countEvents(ics)
		log.Printf("Got iCalendar for %s with %d events", month, event_count)

		// Store the iCalendar data in the map
		icals[month] = ics
	}

	return icals
}

func login(client *http.Client, username, password string) (string, error) {
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
		return "", fmt.Errorf("failed to build login request: %w", err)
	}

	// Set headers to mimic a real browser
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	// Send the POST request
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("login request failed: %w", err)
	}

	// Check for incorrect login
	body, _ := io.ReadAll(resp.Body)
	if incorectLogin(string(body)) {
		return "", errors.New("incorrect username or password")
	}

	defer resp.Body.Close()

	// Check if we got redirected, then the service might be down
	if resp.Request.URL.String() != loginScript {
		return "", fmt.Errorf("unexpected redirect to %s, service may be down", resp.Request.URL.String())
	}

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
		return "", fmt.Errorf("no session cookie received (response URL: %s)", resp.Request.URL.String())
	}

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
				return "", fmt.Errorf("failed to follow redirect: %w", err)
			}
			defer resp.Body.Close()
		}
	}

	// Now, parse the HTML body to extract the sessionId
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %w", err)
	}

	sessionID := extractSessionID(string(body))
	if sessionID == "" {
		return "", errors.New("no session ID found in response")
	}

	return sessionID, nil
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
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	//log.Printf("Response for month %s: %s", date, string(body))

	// Log the response body, headers, and status code if access is denied
	if accessDenied(string(body)) {
		log.Printf("Access denied. Response body: %s", string(body))
		log.Printf("Response headers: %v", resp.Header)
		return "", errors.New("access denied")
	}

	// Log the response body, headers, and status code if no events are found
	if noEvents(string(body)) {
		return "", errors.New("no events")
	}

	link := extractFiletransferLink(string(body))
	if link == "" {
		return "", errors.New("no .ics link found")
	}

	// Download .ics file
	icsResp, err := client.Get(link)
	if err != nil {
		return "", err
	}
	defer icsResp.Body.Close()

	icsData, _ := io.ReadAll(icsResp.Body)

	// Convert UTF-16 to UTF-8
	utf8Data := UTF16ToUTF8(icsData)
	if utf8Data == nil {
		return "", errors.New("error converting UTF-16 to UTF-8")
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

func countEvents(ical string) int {
	// Count the number of events in the iCalendar data
	lines := strings.Split(ical, "\n")
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "BEGIN:VEVENT") {
			count++
		}
	}
	return count
}
