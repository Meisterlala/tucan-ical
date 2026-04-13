package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const tucanAuthorizeURL = "https://dsf.tucan.tu-darmstadt.de/IdentityServer/connect/authorize?client_id=ClassicWeb&scope=openid%20DSF%20email&response_mode=query&response_type=code&ui_locales=de&redirect_uri=https%3a%2f%2fwww.tucan.tu-darmstadt.de%2Fscripts%2Fmgrqispi.dll%3FAPPNAME%3DCampusNet%26PRGNAME%3DLOGINCHECK%26ARGUMENTS%3D-N000000000000001%2Cids_mode%26ids_mode%3DY"

func login(client *http.Client, username, password, totpSeed, totpID string) (string, error) {
	debug := strings.EqualFold(os.Getenv("DEBUG_LOGIN"), "true")
	manualClient := cloneClientNoRedirect(client)

	resp, body, err := doRequest(manualClient, "GET", tucanAuthorizeURL, "", debug)
	if err != nil {
		return "", err
	}

	resp, body, err = followRedirects(manualClient, resp, body, 20, debug)
	if err != nil {
		return "", err
	}

	// Check if this is the TU-ID DFN Shibboleth SSO page
	if strings.Contains(body, "provider=dfnshib") {
		extractSSOURL := regexp.MustCompile(`href=["']([^"']*provider=dfnshib[^"']*)["']`)
		if m := extractSSOURL.FindStringSubmatch(body); len(m) == 2 {
			ssoURL := htmlUnescape(strings.TrimSpace(m[1]))
			ssoURL = resolveURL(resp.Request.URL, ssoURL)
			if debug {
				log.Printf("login debug: following TU-ID SSO link: %s", ssoURL)
			}
			resp, body, err = doRequest(manualClient, "GET", ssoURL, "", debug)
			if err != nil {
				return "", err
			}
			resp, body, err = followRedirects(manualClient, resp, body, 20, debug)
			if err != nil {
				return "", err
			}
		} else {
			return "", errorsWithBody("TU-ID SSO link not found", body)
		}
	}

	csrf := extractInputValue(body, "csrf_token")
	if csrf == "" {
		csrf = extractInputValue(body, "__RequestVerificationToken")
	}
	if csrf == "" {
		return "", errorsWithBody("missing csrf_token/__RequestVerificationToken on login page", body)
	}

	loginAction := extractFormAction(body)
	if loginAction == "" {
		loginAction = resp.Request.URL.String()
	}
	loginURL := resolveURL(resp.Request.URL, loginAction)

	usernameField := "j_username"
	passwordField := "j_password"
	if strings.Contains(body, "name=\"Username\"") {
		usernameField = "Username"
		passwordField = "Password"
	}

	credentials := url.Values{
		"csrf_token":       {csrf},
		usernameField:      {username},
		passwordField:      {password},
		"_eventId_proceed": {""},
	}

	resp, body, err = doRequest(manualClient, "POST", loginURL, credentials.Encode(), debug)
	if err != nil {
		return "", err
	}
	resp, body, err = followRedirects(manualClient, resp, body, 20, debug)
	if err != nil {
		return "", err
	}

	if invalidCredentialsBody(body) {
		return "", errInvalidCredentials
	}

	if !hasSAMLForm(body) && !isSelectTokenPage(body) {
		totpField := detectTotpField(body)
		resp, body, err = submitOTP(manualClient, resp, body, totpSeed, totpField, debug)
		if err != nil {
			return "", err
		}
	}

	if isSelectTokenPage(body) {
		csrf = extractInputValue(body, "csrf_token")
		if csrf == "" {
			return "", errorsWithBody("missing csrf_token on token selection page", body)
		}
		formAction := extractFormAction(body)
		if formAction == "" {
			formAction = resp.Request.URL.String()
		}
		selectionURL := resolveURL(resp.Request.URL, formAction)

		tokens := extractTokenOptions(body)
		if len(tokens) == 0 {
			return "", errorsWithBody("no token options found on token selection page", body)
		}

		desiredID := strings.TrimSpace(totpID)
		if desiredID == "" {
			// first available token
			for id := range tokens {
				desiredID = id
				break
			}
		}
		if _, ok := tokens[desiredID]; !ok {
			var available []string
			for id, name := range tokens {
				available = append(available, fmt.Sprintf("  %s (%s)", id, name))
			}
			return "", fmt.Errorf("TUCAN_TOTP_ID %q not found. Available tokens:\n%s", desiredID, strings.Join(available, "\n"))
		}

		if debug {
			log.Printf("login debug: using token id %q (%s)", desiredID, tokens[desiredID])
		}

		selectForm := url.Values{
			"csrf_token":                     {csrf},
			"fudis_selected_token_ids_input": {desiredID},
			"_eventId_proceed":               {""},
		}
		resp, body, err = doRequest(manualClient, "POST", selectionURL, selectForm.Encode(), debug)
		if err != nil {
			return "", err
		}
		resp, body, err = followRedirects(manualClient, resp, body, 20, debug)
		if err != nil {
			return "", err
		}
	}

	// After token selection, we may land on OTP entry page (fudis_otp_input)
	if debug {
		log.Printf("login debug: OTP check: hasSAML=%v, hasOTPField=%v", hasSAMLForm(body), strings.Contains(body, "fudis_otp_input"))
	}
	if !hasSAMLForm(body) && strings.Contains(body, "fudis_otp_input") {
		resp, body, err = submitOTP(manualClient, resp, body, totpSeed, "fudis_otp_input", debug)
		if err != nil {
			return "", err
		}
	}

	if hasSAMLForm(body) {
		samlAction := extractFormAction(body)
		samlResponse := extractInputValue(body, "SAMLResponse")
		relayState := extractInputValue(body, "RelayState")
		if samlAction == "" || samlResponse == "" {
			return "", errorsWithBody("missing SAML handover fields", body)
		}

		samlURL := resolveURL(resp.Request.URL, samlAction)
		samlForm := url.Values{"SAMLResponse": {samlResponse}}
		if relayState != "" {
			samlForm.Set("RelayState", relayState)
		}

		resp, body, err = doRequest(manualClient, "POST", samlURL, samlForm.Encode(), debug)
		if err != nil {
			return "", err
		}
		resp, body, err = followRedirects(manualClient, resp, body, 20, debug)
		if err != nil {
			return "", err
		}
	}

	sessionID := extractSessionID(body)
	if sessionID == "" && resp != nil {
		sessionID = extractSessionIDFromRefresh(resp.Header.Get("Refresh"))
	}
	if sessionID == "" && resp != nil {
		sessionID = extractCookieValueForURL(client, resp.Request.URL, "cnsc")
	}
	if sessionID == "" {
		fallbackURL, err := url.Parse(loginScript)
		if err == nil {
			sessionID = extractCookieValueForURL(client, fallbackURL, "cnsc")
		}
	}
	if sessionID == "" {
		return "", errorsWithBody("no session ID found after login", body)
	}

	return sessionID, nil
}

func cloneClientNoRedirect(client *http.Client) *http.Client {
	cloned := *client
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &cloned
}

func doRequest(client *http.Client, method, rawURL, body string, debug bool) (*http.Response, string, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build %s request: %w", method, err)
	}
	req.Header.Set("User-Agent", userAgent)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%s request failed: %w", method, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response body: %w", err)
	}
	bodyText := string(rawBody)

	if debug {
		location := resp.Header.Get("Location")
		log.Printf("login debug: %s %s -> %d (location=%q)", method, rawURL, resp.StatusCode, location)
		if len(bodyText) > 0 {
			log.Printf("login debug response body:\n%s", bodyText)
		}
	}

	return resp, bodyText, nil
}

func followRedirects(client *http.Client, resp *http.Response, body string, maxRedirects int, debug bool) (*http.Response, string, error) {
	currentResp := resp
	currentBody := body
	for i := 0; i < maxRedirects; i++ {
		if currentResp.StatusCode < 300 || currentResp.StatusCode > 399 {
			return currentResp, currentBody, nil
		}

		location := currentResp.Header.Get("Location")
		if location == "" {
			return currentResp, currentBody, nil
		}

		nextURL := resolveURL(currentResp.Request.URL, location)
		method := "GET"
		if currentResp.StatusCode == http.StatusTemporaryRedirect || currentResp.StatusCode == http.StatusPermanentRedirect {
			method = currentResp.Request.Method
		}

		if debug {
			log.Printf("login debug: follow redirect -> %s", nextURL)
		}

		nextResp, nextBody, err := doRequest(client, method, nextURL, "", debug)
		if err != nil {
			return nil, "", err
		}
		currentResp = nextResp
		currentBody = nextBody
	}

	return nil, "", fmt.Errorf("too many redirects")
}

func resolveURL(base *url.URL, target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	return base.ResolveReference(u).String()
}

func extractInputValue(body, name string) string {
	re := regexp.MustCompile(`name=["']` + regexp.QuoteMeta(name) + `["'][^>]*value=["']([^"']*)["']`)
	if m := re.FindStringSubmatch(body); len(m) == 2 {
		return htmlUnescape(m[1])
	}
	re = regexp.MustCompile(`value=["']([^"']*)["'][^>]*name=["']` + regexp.QuoteMeta(name) + `["']`)
	if m := re.FindStringSubmatch(body); len(m) == 2 {
		return htmlUnescape(m[1])
	}
	return ""
}

func extractFormAction(body string) string {
	re := regexp.MustCompile(`(?is)<form[^>]*action=["']([^"']+)["']`)
	if m := re.FindStringSubmatch(body); len(m) == 2 {
		return htmlUnescape(strings.TrimSpace(m[1]))
	}
	return ""
}

func detectTotpField(body string) string {
	candidates := []string{
		"fudis_otp_input",
		"j_tokenNumber",
		"token",
		"otp",
		"totp",
		"verificationCode",
		"j_otp",
	}
	for _, candidate := range candidates {
		if strings.Contains(body, `name="`+candidate+`"`) || strings.Contains(body, `name='`+candidate+`'`) {
			return candidate
		}
	}
	return "fudis_otp_input"
}

func submitOTP(client *http.Client, resp *http.Response, body, totpSeed, field string, debug bool) (*http.Response, string, error) {
	currentResp := resp
	currentBody := body

	for _, totp := range otpCandidates(time.Now(), totpSeed) {
		csrf := extractInputValue(currentBody, "csrf_token")
		if csrf == "" {
			return nil, "", errorsWithBody("missing csrf_token on OTP page", currentBody)
		}

		action := extractFormAction(currentBody)
		if action == "" {
			action = currentResp.Request.URL.String()
		}
		otpURL := resolveURL(currentResp.Request.URL, action)

		form := url.Values{
			"csrf_token":       {csrf},
			"_eventId_proceed": {""},
			field:              {totp},
		}

		var err error
		currentResp, currentBody, err = doRequest(client, "POST", otpURL, form.Encode(), debug)
		if err != nil {
			return nil, "", err
		}
		currentResp, currentBody, err = followRedirects(client, currentResp, currentBody, 20, debug)
		if err != nil {
			return nil, "", err
		}
		if !invalidOTPBody(currentBody) {
			return currentResp, currentBody, nil
		}
	}

	return currentResp, currentBody, nil
}

func otpCandidates(now time.Time, totpSeed string) []string {
	var candidates []string
	seen := make(map[string]bool)
	for _, ts := range []time.Time{now, now.Add(-30 * time.Second), now.Add(30 * time.Second)} {
		code := calculate_totp(totpSeed, ts)
		if code != "" && !seen[code] {
			seen[code] = true
			candidates = append(candidates, code)
		}
	}
	return candidates
}

func hasSAMLForm(body string) bool {
	return strings.Contains(body, "name=\"SAMLResponse\"") || strings.Contains(body, "name='SAMLResponse'")
}

func isSelectTokenPage(body string) bool {
	return strings.Contains(body, "fudis_selected_token_ids_input")
}

func extractTokenOptions(body string) map[string]string {
	tokens := make(map[string]string)
	re := regexp.MustCompile(`<option[^>]*value=["']([^"']+)["'][^>]*>([^<]*)</option>`)
	matches := re.FindAllStringSubmatch(body, -1)
	for _, m := range matches {
		if len(m) >= 3 {
			id := strings.TrimSpace(m[1])
			name := strings.TrimSpace(m[2])
			if id != "" {
				tokens[id] = name
			}
		}
	}
	return tokens
}

func extractSessionID(body string) string {
	re := regexp.MustCompile(`(?is)<div[^>]*id=["']sessionId["'][^>]*>\s*([^<\s]+)\s*</div>`)
	if m := re.FindStringSubmatch(body); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func extractSessionIDFromRefresh(refresh string) string {
	re := regexp.MustCompile(`(?i)ARGUMENTS=-N([0-9]+)`)
	if m := re.FindStringSubmatch(refresh); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func invalidCredentialsBody(body string) bool {
	if incorectLogin(body) {
		return true
	}
	markers := []string{
		"The username you entered cannot be identified or the password you entered was incorrect.",
		"Bitte versuchen Sie es erneut",
		"Nutzername oder Passwort",
		"Anmeldung fehlgeschlagen",
	}
	for _, marker := range markers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func invalidOTPBody(body string) bool {
	markers := []string{
		"Verification invalid",
		"Verifikation",
	}
	for _, marker := range markers {
		if strings.Contains(body, marker) && strings.Contains(body, "output--error") {
			return true
		}
	}
	return false
}

func errorsWithBody(prefix, body string) error {
	trimmed := strings.TrimSpace(body)
	if len(trimmed) > 300 {
		trimmed = trimmed[:300]
	}
	return fmt.Errorf("%s: %s", prefix, trimmed)
}

func htmlUnescape(value string) string {
	return html.UnescapeString(value)
}

func extractCookieValueForURL(client *http.Client, u *url.URL, name string) string {
	if client == nil || client.Jar == nil {
		return ""
	}
	if u == nil {
		return ""
	}

	for _, cookie := range client.Jar.Cookies(u) {
		if cookie.Name == name {
			return cookie.Value
		}
	}

	return ""
}

func calculate_totp(staticCode string, now time.Time) string {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(staticCode), " ", ""))
	decoder := base32.StdEncoding.WithPadding(base32.NoPadding)
	secret, err := decoder.DecodeString(normalized)
	if err != nil || len(secret) == 0 {
		return ""
	}

	counter := uint64(now.Unix() / 30)
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)

	mac := hmac.New(sha1.New, secret)
	mac.Write(counterBytes[:])
	hash := mac.Sum(nil)

	offset := hash[len(hash)-1] & 0x0f
	binaryCode := (int(hash[offset])&0x7f)<<24 |
		(int(hash[offset+1])&0xff)<<16 |
		(int(hash[offset+2])&0xff)<<8 |
		(int(hash[offset+3]) & 0xff)

	code := binaryCode % 1000000
	result := strconv.Itoa(code)
	for len(result) < 6 {
		result = "0" + result
	}
	return result
}
