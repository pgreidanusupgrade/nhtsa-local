package main

// Cross-check tests: compare our decoder against the NHTSA VPIC REST API.
//
// Offline mode (default): uses nhtsaGolden, a hardcoded snapshot of NHTSA
// results for the probe VINs. Runs in CI without network access.
//
// Live mode: set NHTSA_LIVE_TEST=1 to call the real API. Use this when the
// VPIC database is refreshed to verify our output still matches upstream.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var nhtsaGolden = map[string]struct {
	Make      string
	ModelYear string
}{
	"1HGCM82633A004352": {Make: "HONDA", ModelYear: "2003"},
	"1FTFW1ET5EKE52261": {Make: "FORD", ModelYear: "2014"},
	"2T1BURHE0JC060752": {Make: "TOYOTA", ModelYear: "2018"},
}

type nhtsaAPIResponse struct {
	Results []struct {
		Variable string `json:"Variable"`
		Value    string `json:"Value"`
	} `json:"Results"`
}

func fetchNHTSA(vin string) (make_, year string, err error) {
	url := "https://vpic.nhtsa.dot.gov/api/vehicles/DecodeVin/" + vin + "?format=json"
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	var body nhtsaAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("decode json: %w", err)
	}

	for _, item := range body.Results {
		if item.Value == "" || item.Value == "Not Applicable" {
			continue
		}
		switch item.Variable {
		case "Make":
			make_ = strings.ToUpper(item.Value)
		case "Model Year":
			year = item.Value
		}
	}
	return make_, year, nil
}

// TestNHTSAGoldenComparison verifies our decoder matches stored NHTSA results.
// Offline — no network required.
func TestNHTSAGoldenComparison(t *testing.T) {
	loadTestData(t)
	for vin, golden := range nhtsaGolden {
		vin, golden := vin, golden
		t.Run(vin, func(t *testing.T) {
			res, err := decodeVIN(vin)
			if err != nil {
				t.Fatalf("decodeVIN: %v", err)
			}
			if got := strings.ToUpper(res.Make); got != golden.Make {
				t.Errorf("Make: golden=%q got=%q", golden.Make, got)
			}
			if res.ModelYear != golden.ModelYear {
				t.Errorf("ModelYear: golden=%q got=%q", golden.ModelYear, res.ModelYear)
			}
		})
	}
}

// TestNHTSALiveComparison calls the real NHTSA API and compares results.
// Only runs when NHTSA_LIVE_TEST=1 — requires internet access.
//
//	NHTSA_LIVE_TEST=1 go test -v -run TestNHTSALiveComparison
func TestNHTSALiveComparison(t *testing.T) {
	if os.Getenv("NHTSA_LIVE_TEST") != "1" {
		t.Skip("set NHTSA_LIVE_TEST=1 to run live API comparison")
	}
	loadTestData(t)

	vins := []string{
		"1HGCM82633A004352",
		"1FTFW1ET5EKE52261",
		"2T1BURHE0JC060752",
		"WBA3A5G59DNP26082",
		"JTEBU5JR5G5375843",
		"5NPE24AF8FH213670",
	}

	for _, vin := range vins {
		vin := vin
		t.Run(vin, func(t *testing.T) {
			nhtsaMake, nhtsaYear, err := fetchNHTSA(vin)
			if err != nil {
				t.Fatalf("NHTSA API: %v", err)
			}
			if nhtsaMake == "" {
				t.Fatalf("NHTSA returned empty Make for %s", vin)
			}

			res, err := decodeVIN(vin)
			if err != nil {
				t.Fatalf("decodeVIN: %v", err)
			}

			if got := strings.ToUpper(res.Make); got != nhtsaMake {
				t.Errorf("Make: NHTSA=%q got=%q", nhtsaMake, got)
			}
			if nhtsaYear != "" && res.ModelYear != nhtsaYear {
				t.Errorf("ModelYear: NHTSA=%q got=%q", nhtsaYear, res.ModelYear)
			}
			t.Logf("NHTSA Make=%q Year=%q | ours Make=%q Year=%q", nhtsaMake, nhtsaYear, res.Make, res.ModelYear)
		})
	}
}
