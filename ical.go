package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

var lastNewestCalendarGetOK atomic.Bool

var errNoEvents = errors.New("no events")
var errInvalidCredentials = errors.New("incorrect username or password")

func startCalendarUpdater(username, password, totpSeed, totpID string, update_interval time.Duration) {
	ticker := time.NewTicker(update_interval)
	icals := make(map[string]string)
	consecutiveInvalidLogins := 0

	defer ticker.Stop()

	for {
		log.Println("Updating calendar...")

		// Fetch iCalendar data
		newIcals, err := fetchIcalData(username, password, totpSeed, totpID)
		if err != nil {
			if errors.Is(err, errInvalidCredentials) {
				consecutiveInvalidLogins++
				if consecutiveInvalidLogins >= 2 {
					log.Printf("Login failed twice in a row with invalid credentials, exiting: %v", err)
					os.Exit(1)
				}
			} else {
				consecutiveInvalidLogins = 0
			}

			<-ticker.C
			continue
		}
		consecutiveInvalidLogins = 0

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

func fetchIcalData(username, password, totpSeed, totpID string) (map[string]string, error) {
	icals := make(map[string]string)
	lastNewestCalendarGetOK.Store(false)

	// Create a new client with a cookie jar
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Log in with the client and get the session cookie
	session, err := login(client, username, password, totpSeed, totpID)
	if err != nil {
		log.Printf("Login failed: %v", err)
		lastNewestCalendarGetOK.Store(false)
		return icals, err
	}

	const newestMonthOffset = 7
	for i := -3; i <= newestMonthOffset; i++ {
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
			if i == newestMonthOffset {
				lastNewestCalendarGetOK.Store(errors.Is(err, errNoEvents))
			}
			log.Printf("Error getting iCalendar for %s: %v", month, err)
			continue
		}
		if ics == "" {
			if i == newestMonthOffset {
				lastNewestCalendarGetOK.Store(false)
			}
			log.Printf("No iCalendar data for %s", month)
			continue
		}
		if i == newestMonthOffset {
			lastNewestCalendarGetOK.Store(true)
		}

		event_count := countEvents(ics)
		log.Printf("Got iCalendar for %s with %d events", month, event_count)

		// Store the iCalendar data in the map
		icals[month] = ics
	}

	return icals, nil
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

	// Log the response body, headers, and status code if access is denied
	if accessDenied(string(body)) {
		log.Printf("Access denied. Response body: %s", string(body))
		log.Printf("Response headers: %v", resp.Header)
		return "", errors.New("access denied")
	}

	// Log the response body, headers, and status code if no events are found
	if noEvents(string(body)) {
		return "", errNoEvents
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
