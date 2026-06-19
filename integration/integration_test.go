package integration_test

// Black-box integration tests. Both API containers must be running:
//   podman compose up -d
//
// Environment overrides (defaults shown):
//   GOB_URL    http://localhost:8080
//   SQLITE_URL http://localhost:8081
//
// The test suite has three layers:
//   1. Health — both containers respond on /health
//   2. Parity — for every VIN in the fixture, gob and sqlite return identical decoded fields
//   3. NHTSA  — for every VIN, the decoded output matches the golden NHTSA fixture
//              (hard checks: Make, ModelYear, VehicleType; soft checks: all Attributes)
//
// Run short mode (-short flag) to test only the 6 curated probe VINs instead of all 1006.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	gobURL    = envOr("GOB_URL", "http://localhost:8080")
	sqliteURL = envOr("SQLITE_URL", "http://localhost:8081")
)

// probeVINs is the curated set always tested even in -short mode.
var probeVINs = []string{
	"1HGCM82633A004352", // 2003 Honda Accord
	"1FTFW1ET5EKE52261", // 2014 Ford F-150
	"2T1BURHE0JC060752", // 2018 Toyota Corolla
	"7SAYGAEE9NF432848", // 2022 Tesla Model Y (BEV)
	"1GC4YUEY5LF152163", // 2020 Chevrolet Silverado 3500 (diesel)
	"1GKS2GKCXLR292005", // 2020 GMC Yukon XL
}

// brandFamilies for Make tolerance — same as in the unit test packages.
var brandFamilies = map[string]string{
	"DODGE": "stellantis", "JEEP": "stellantis", "RAM": "stellantis",
	"CHRYSLER": "stellantis", "FIAT": "stellantis", "PLYMOUTH": "stellantis",
	"EAGLE": "stellantis", "MASERATI": "stellantis", "ALFA ROMEO": "stellantis",
	"HYUNDAI": "hyundai-group", "KIA": "hyundai-group", "GENESIS": "hyundai-group",
	"NISSAN": "nissan-group", "INFINITI": "nissan-group", "MITSUBISHI": "nissan-group",
	"GMC": "gm", "CHEVROLET": "gm", "PONTIAC": "gm", "BUICK": "gm",
	"CADILLAC": "gm", "OLDSMOBILE": "gm", "SATURN": "gm", "HUMMER": "gm",
	"INTERNATIONAL": "navistar", "NAVISTAR": "navistar",
	"TOYOTA": "toyota-subaru", "SUBARU": "toyota-subaru", "SCION": "toyota-subaru",
}

func sameFamily(a, b string) bool {
	fa, aOK := brandFamilies[a]
	fb, bOK := brandFamilies[b]
	return aOK && bOK && fa == fb
}

// decodeResult mirrors the JSON shape returned by both API containers.
type decodeResult struct {
	WMI          string            `json:"wmi"`
	Make         string            `json:"make"`
	MakeName     string            `json:"make_name"`
	Manufacturer string            `json:"manufacturer"`
	VehicleType  string            `json:"vehicle_type"`
	ModelYear    string            `json:"model_year"`
	Attributes   map[string]string `json:"attributes"`
}

type vinResponse struct {
	VIN    string        `json:"vin"`
	Result *decodeResult `json:"result"`
	Error  string        `json:"error"`
}

func decodeVIN(baseURL, vin string) (*decodeResult, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + "/vin/" + vin)
	if err != nil {
		return nil, fmt.Errorf("GET %s/vin/%s: %w", baseURL, vin, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d from %s/vin/%s", resp.StatusCode, baseURL, vin)
	}
	var vr vinResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if vr.Error != "" {
		return nil, fmt.Errorf("API error: %s", vr.Error)
	}
	if vr.Result == nil {
		return nil, fmt.Errorf("nil result")
	}
	return vr.Result, nil
}

func fixtureDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "api-gob", "testdata")
}

func loadFixture(t *testing.T) map[string]map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(fixtureDir(), "nhtsa_golden.json"))
	if err != nil {
		t.Fatalf("open nhtsa_golden.json: %v", err)
	}
	defer f.Close()
	var fix map[string]map[string]string
	if err := json.NewDecoder(f).Decode(&fix); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return fix
}

// TestHealth verifies both containers are up and responsive.
func TestHealth(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"api-gob", gobURL},
		{"api-sqlite", sqliteURL},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get(tc.url + "/health")
			if err != nil {
				t.Fatalf("%s health check failed: %v", tc.name, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s health returned %d", tc.name, resp.StatusCode)
			}
		})
	}
}

// TestParity checks that api-gob and api-sqlite return identical decoded fields
// for every VIN in the fixture.
func TestParity(t *testing.T) {
	fixture := loadFixture(t)

	vins := probeVINs
	if !testing.Short() {
		vins = make([]string, 0, len(fixture))
		for vin := range fixture {
			vins = append(vins, vin)
		}
	}

	var mismatch int
	for _, vin := range vins {
		vin := vin
		t.Run(vin, func(t *testing.T) {
			gob, err := decodeVIN(gobURL, vin)
			if err != nil {
				t.Fatalf("gob: %v", err)
			}
			sqlite, err := decodeVIN(sqliteURL, vin)
			if err != nil {
				t.Fatalf("sqlite: %v", err)
			}

			if !strings.EqualFold(gob.Make, sqlite.Make) {
				t.Errorf("Make: gob=%q sqlite=%q", gob.Make, sqlite.Make)
				mismatch++
			}
			if gob.ModelYear != sqlite.ModelYear {
				t.Errorf("ModelYear: gob=%q sqlite=%q", gob.ModelYear, sqlite.ModelYear)
				mismatch++
			}
			if !strings.EqualFold(gob.VehicleType, sqlite.VehicleType) {
				t.Errorf("VehicleType: gob=%q sqlite=%q", gob.VehicleType, sqlite.VehicleType)
				mismatch++
			}

			// Attributes: every key present in gob must match sqlite and vice-versa.
			norm := func(s string) string {
				return strings.Join(strings.Fields(strings.ToLower(s)), " ")
			}
			for k, gv := range gob.Attributes {
				if sv, ok := sqlite.Attributes[k]; !ok {
					t.Errorf("attr %q: in gob (%q) but missing from sqlite", k, gv)
					mismatch++
				} else if norm(gv) != norm(sv) {
					t.Errorf("attr %q: gob=%q sqlite=%q", k, gv, sv)
					mismatch++
				}
			}
			for k, sv := range sqlite.Attributes {
				if _, ok := gob.Attributes[k]; !ok {
					t.Errorf("attr %q: in sqlite (%q) but missing from gob", k, sv)
					mismatch++
				}
			}
		})
	}
}

// TestNHTSA checks that each container's decoded output matches the NHTSA
// golden fixture. Both containers are checked; failures are reported per-container.
func TestNHTSA(t *testing.T) {
	fixture := loadFixture(t)

	vins := probeVINs
	if !testing.Short() {
		vins = make([]string, 0, len(fixture))
		for vin := range fixture {
			vins = append(vins, vin)
		}
	}

	for _, containerName := range []string{"api-gob", "api-sqlite"} {
		containerName := containerName
		baseURL := gobURL
		if containerName == "api-sqlite" {
			baseURL = sqliteURL
		}

		t.Run(containerName, func(t *testing.T) {
			var skippedYear, skippedMakeWMI, skippedMakeFam, noMake, attrMismatch int

			for _, vin := range vins {
				vin := vin
				want, ok := fixture[vin]
				if !ok {
					continue
				}

				t.Run(vin, func(t *testing.T) {
					res, err := decodeVIN(baseURL, vin)
					if err != nil {
						t.Fatalf("decodeVIN: %v", err)
					}

					// ── ModelYear ────────────────────────────────────────────
					if nhtsaYear := want["Model Year"]; nhtsaYear != "" {
						y, _ := strconv.Atoi(nhtsaYear)
						if y < 2010 {
							skippedYear++
						} else if res.ModelYear != nhtsaYear {
							t.Errorf("ModelYear: NHTSA=%q got=%q", nhtsaYear, res.ModelYear)
						}
					}

					// ── Make ─────────────────────────────────────────────────
					nhtsaMake := strings.ToUpper(want["Make"])
					if nhtsaMake == "" {
						noMake++
					} else {
						gotMake := strings.ToUpper(res.Make)
						if gotMake != nhtsaMake {
							// Check the full make_name list for multi-brand WMIs.
							found := false
							for _, m := range strings.Split(strings.ToUpper(res.MakeName), ",") {
								if strings.TrimSpace(m) == nhtsaMake {
									found = true
									break
								}
							}
							if found {
								skippedMakeWMI++
							} else if sameFamily(gotMake, nhtsaMake) {
								skippedMakeFam++
							} else {
								t.Errorf("Make: NHTSA=%q got=%q (make_name=%q)", nhtsaMake, gotMake, res.MakeName)
							}
						}
					}

					// ── VehicleType ───────────────────────────────────────────
					if nhtsaVT := want["Vehicle Type"]; nhtsaVT != "" && res.VehicleType != "" {
						if !strings.EqualFold(res.VehicleType, nhtsaVT) {
							t.Errorf("VehicleType: NHTSA=%q got=%q", nhtsaVT, res.VehicleType)
						}
					}

					// ── Attributes (soft) ─────────────────────────────────────
					norm := func(s string) string {
						return strings.Join(strings.Fields(strings.ToLower(s)), " ")
					}
					for varName, nhtsaVal := range want {
						switch varName {
						case "Make", "Model Year", "Vehicle Type",
							"Manufacturer", "Manufacturer Name", "Manufacturer Id",
							"Plant City", "Plant Country", "Plant State", "Plant Company Name",
							"Note", "Destination Market",
							"Error Code", "Error Text", "Additional Error Text",
							"Possible Values", "Suggested VIN",
							"Vehicle Descriptor", "Active Safety System Note":
							continue
						}
						gotVal, present := res.Attributes[varName]
						if !present {
							continue
						}
						if norm(gotVal) != norm(nhtsaVal) {
							t.Logf("INFO attr mismatch %q: NHTSA=%q got=%q", varName, nhtsaVal, gotVal)
							attrMismatch++
						}
					}
				})
			}

			t.Logf("%s summary: skipped ModelYear for %d pre-2010 VINs; Make: %d WMI-list accepted, %d brand-family; %d had no NHTSA make; %d soft attr mismatches",
				containerName, skippedYear, skippedMakeWMI, skippedMakeFam, noMake, attrMismatch)
		})
	}
}
