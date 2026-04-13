package main

import (
	"net/http"
	"net/http/cookiejar"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

func TestLogin(t *testing.T) {
	godotenv.Load()

	username := os.Getenv("TUCAN_USERNAME")
	password := os.Getenv("TUCAN_PASSWORD")
	totpSeed := os.Getenv("TUCAN_TOTP")
	totpID := os.Getenv("TUCAN_TOTP_ID")

	if username == "" || password == "" || totpSeed == "" || totpID == "" {
		t.Skip("TUCAN_USERNAME, TUCAN_PASSWORD, TUCAN_TOTP and TUCAN_TOTP_ID must be set to run login test")
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	sessionID, err := login(client, username, password, totpSeed, totpID)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	if sessionID == "" {
		t.Fatal("login returned empty session ID")
	}

	t.Logf("login succeeded, session ID: %s", sessionID)
}

func TestCalculateTOTP(t *testing.T) {
	// Test with known values for deterministic verification
	seed := "JBSWY3DPEHPK3PXP"
	frozenTime := time.Unix(0, 0)
	expected := "282760"
	code := calculate_totp(seed, frozenTime)
	if code == "" {
		t.Fatal("calculate_totp returned empty")
	}
	if len(code) != 6 {
		t.Fatalf("expected 6-digit code, got %q", code)
	}
	if code != expected {
		t.Fatalf("expected TOTP %s, got %s", expected, code)
	}
	t.Logf("TOTP at time %d: %s", frozenTime.Unix(), code)
}

func TestCalculateTOTP_MultipleTimes(t *testing.T) {
	// Test that code changes over time intervals (30 seconds)
	seed := "JBSWY3DPEHPK3PXP"

	// At time 0 (counter 0)
	code0 := calculate_totp(seed, time.Unix(0, 0))

	// At time 29 (still counter 0)
	code29 := calculate_totp(seed, time.Unix(29, 0))

	// At time 30 (counter 1)
	code30 := calculate_totp(seed, time.Unix(30, 0))

	if code0 != code29 {
		t.Fatalf("codes within same 30s window should match: %s vs %s", code0, code29)
	}
	if code0 == code30 {
		t.Fatalf("codes in different 30s windows should differ: both %s", code0)
	}

	t.Logf("code0=%s, code29=%s, code30=%s", code0, code29, code30)
}

func TestExtractSessionIDFromRefresh(t *testing.T) {
	refresh := "0;URL=/scripts/mgrqispi.dll?APPNAME=CampusNet&PRGNAME=STARTPAGE_DISPATCH&ARGUMENTS=-N241551323091407,-N000019,-N000000000000000"

	got := extractSessionIDFromRefresh(refresh)
	if got != "241551323091407" {
		t.Fatalf("expected session ID from refresh header, got %q", got)
	}
}

func TestExtractSessionIDFromBody(t *testing.T) {
	body := `<html><body><div id="sessionId" style="display:none;">000000000000001</div></body></html>`

	got := extractSessionID(body)
	if got != "000000000000001" {
		t.Fatalf("expected session ID from body, got %q", got)
	}
}

func TestInvalidCredentialsBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "english error message",
			body: `<div class="output output--error">The username you entered cannot be identified or the password you entered was incorrect.</div>`,
			want: true,
		},
		{
			name: "legacy german error message",
			body: `<p>Bitte versuchen Sie es erneut. Überprüfen Sie ggf. Ihre Zugangsdaten.</p>`,
			want: true,
		},
		{
			name: "non error body",
			body: `<form><input name="csrf_token" value="abc"></form>`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := invalidCredentialsBody(tt.body)
			if got != tt.want {
				t.Fatalf("invalidCredentialsBody() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInvalidOTPBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "english otp error",
			body: `<div class="output output--error">Verification invalid</div>`,
			want: true,
		},
		{
			name: "success page",
			body: `<form><input name="SAMLResponse" value="ok"></form>`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := invalidOTPBody(tt.body)
			if got != tt.want {
				t.Fatalf("invalidOTPBody() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractTokenOptions(t *testing.T) {
	body := `<select name="fudis_selected_token_ids_input"><option value="id-1">Phone</option><option value="id-2">Auth App</option></select>`

	got := extractTokenOptions(body)
	if len(got) != 2 {
		t.Fatalf("expected 2 token options, got %d", len(got))
	}
	if got["id-1"] != "Phone" {
		t.Fatalf("expected id-1 to map to Phone, got %q", got["id-1"])
	}
	if got["id-2"] != "Auth App" {
		t.Fatalf("expected id-2 to map to Auth App, got %q", got["id-2"])
	}
}

func TestDetectTotpField(t *testing.T) {
	body := `<form><input type="text" name="verificationCode"></form>`

	got := detectTotpField(body)
	if got != "verificationCode" {
		t.Fatalf("expected verificationCode field, got %q", got)
	}
}

func TestOtpCandidatesDeduplicatesWindowValues(t *testing.T) {
	now := time.Unix(29, 0)
	got := otpCandidates(now, "JBSWY3DPEHPK3PXP")

	if len(got) < 2 || len(got) > 3 {
		t.Fatalf("expected 2 or 3 candidate OTPs, got %d: %v", len(got), got)
	}

	seen := make(map[string]bool)
	for _, code := range got {
		if seen[code] {
			t.Fatalf("duplicate OTP candidate found: %q in %v", code, got)
		}
		seen[code] = true
	}
}
