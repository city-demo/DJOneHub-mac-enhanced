package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseGPSLocation(t *testing.T) {
	fix, err := parseGPSLocation("AT+QGPSLOC=2\r\n+QGPSLOC: 120000.0,39.900000,116.400000,0.8,42.1,1,0.0,0.0,0.0,01012026,12\r\nOK")
	if err != nil {
		t.Fatalf("parseGPSLocation() error = %v", err)
	}
	if fix.Latitude != "39.900000" || fix.Longitude != "116.400000" || fix.Satellites != "12" {
		t.Fatalf("parseGPSLocation() = %+v", fix)
	}
}

func TestParseGPSLocationWithoutFix(t *testing.T) {
	if _, err := parseGPSLocation("AT+QGPSLOC=2\r\nERROR"); err == nil {
		t.Fatal("parseGPSLocation() error = nil, want no-fix error")
	}
}

func TestHasCMEErrorCode(t *testing.T) {
	if !hasCMEErrorCode(errors.New("设备返回错误:+CME ERROR:504"), "", "504") {
		t.Fatal("expected +CME ERROR:504 to match 504")
	}
	if !hasCMEErrorCode(nil, "+CME ERROR: 505\r\n", "505") {
		t.Fatal("expected +CME ERROR: 505 response to match 505")
	}
	if hasCMEErrorCode(errors.New("设备返回错误:+CME ERROR:516"), "", "504") {
		t.Fatal("did not expect 516 to match 504")
	}
}

func TestGPSATCommandOKHandlesModemStrippedOKAndUSBAT(t *testing.T) {
	if !gpsATCommandOK(true, "", nil) {
		t.Fatal("expected modemMode=true with nil err to succeed even when OK was stripped")
	}
	if gpsATCommandOK(true, "+CME ERROR: 504", nil) {
		t.Fatal("expected modem error response to fail")
	}
	if gpsATCommandOK(true, "", errors.New("boom")) {
		t.Fatal("expected non-nil error to fail")
	}
	if !gpsATCommandOK(false, "OK", nil) {
		t.Fatal("expected usbAT OK to succeed")
	}
	if gpsATCommandOK(false, "", nil) || gpsATCommandOK(false, "ERROR", nil) {
		t.Fatal("expected usbAT empty or ERROR response to fail")
	}
}

func TestParseGSVSatellitesInView(t *testing.T) {
	liveResp := "+QGPSGNMEA: $GPGSV,1,1,02,40,,,34,41,,,35,1*66\r\nOK"
	count, ok := parseGSVSatellitesInView(liveResp)
	if !ok || count != 2 {
		t.Fatalf("parseGSVSatellitesInView(liveResp) = (%d, %v), want (2, true)", count, ok)
	}

	multiResp := strings.Join([]string{
		"+QGPSGNMEA: $GPGSV,2,1,06,01,40,120,30,02,35,210,28*7A",
		"+QGPSGNMEA: $GPGSV,2,2,06,03,20,090,25*7B",
		"+QGPSGNMEA: $BDGSV,1,1,04,11,50,180,32*6C",
	}, "\n")
	count, ok = parseGSVSatellitesInView(multiResp)
	if !ok || count != 10 {
		t.Fatalf("parseGSVSatellitesInView(multiResp) = (%d, %v), want (10, true)", count, ok)
	}

	if _, ok := parseGSVSatellitesInView("OK"); ok {
		t.Fatal("expected OK without GSV sentence to return ok=false")
	}
}
