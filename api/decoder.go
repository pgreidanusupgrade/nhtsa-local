package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// decodeResult holds all decoded attributes for a VIN.
type decodeResult struct {
	WMI         string            `json:"wmi"`
	Make        string            `json:"make,omitempty"`
	MakeName    string            `json:"make_name,omitempty"`
	Manufacturer string           `json:"manufacturer,omitempty"`
	VehicleType string            `json:"vehicle_type,omitempty"`
	ModelYear   string            `json:"model_year,omitempty"`
	Attributes  map[string]string `json:"attributes"`
}

// vinWMI implements vpic.fVinWMI: first 3 chars, extended to 6 if pos 3 is '9'.
// Small-volume manufacturers use a 6-char WMI (VIN[0:3] + VIN[11:14]).
func vinWMI(vin string) string {
	if len(vin) < 3 {
		return vin
	}
	wmi := vin[:3]
	if wmi[2] == '9' && len(vin) >= 14 {
		wmi = wmi + vin[11:14]
	}
	return wmi
}

// vinKey builds the key string the NHTSA pattern matching uses:
// positions 4-8 (VDS) concatenated with "|" and positions 10-17 (VIS).
// Source: vpic.spvindecode_core: SUBSTRING(var_vin,4,5) || '|' || SUBSTRING(var_vin,10,8)
func vinKey(vin string) string {
	if len(vin) < 9 {
		return vin[3:]
	}
	return vin[3:8] + "|" + vin[9:]
}

// vinModelYear implements vpic.fVinModelYear2 without the vehicle-type disambiguation.
// Returns 0 if the year character at VIN[9] is invalid.
//
// The year character at position 10 (index 9) of the VIN encodes model year.
// Letters repeat every 30 years (A=1980/2010, B=1981/2011, ...). Digits 1-9
// encode 2001-2009 (first cycle) / 2031-2039 (second cycle).
// If the decoded year is more than 2 years in the future, subtract 30.
func vinModelYear(vin string) int {
	if len(vin) < 10 {
		return 0
	}
	pos10 := vin[9]
	var y int
	switch {
	case pos10 >= 'A' && pos10 <= 'H':
		y = 2010 + int(pos10-'A')
	case pos10 >= 'J' && pos10 <= 'N':
		y = 2010 + int(pos10-'A') - 1
	case pos10 == 'P':
		y = 2023
	case pos10 >= 'R' && pos10 <= 'T':
		y = 2010 + int(pos10-'A') - 3
	case pos10 >= 'V' && pos10 <= 'Y':
		y = 2010 + int(pos10-'A') - 4
	case pos10 >= '1' && pos10 <= '9':
		y = 2031 + int(pos10-'1')
	default:
		return 0
	}
	limit := time.Now().Year() + 2
	if y > limit {
		y -= 30
	}
	return y
}

// decodeVIN decodes a VIN using the in-memory lookup tables populated by
// loadVPICData. No database access occurs after startup.
func decodeVIN(vin string) (*decodeResult, error) {
	vin = strings.ToUpper(vin)
	if len(vin) < 3 {
		return nil, fmt.Errorf("VIN too short")
	}

	wmi := vinWMI(vin)
	key := vinKey(vin)

	res := &decodeResult{
		WMI:        wmi,
		Attributes: map[string]string{},
	}

	// 1. WMI-level attributes: Make, Manufacturer, VehicleType.
	if entry, ok := wmiTable[wmi]; ok {
		if entry.MakeNames != "" {
			parts := strings.SplitN(entry.MakeNames, ",", 2)
			res.Make = parts[0]
			res.MakeName = entry.MakeNames
		}
		res.Manufacturer = entry.MfrName
		res.VehicleType = entry.VehicleType
	}

	// 2. ModelYear from VIN position 10.
	if y := vinModelYear(vin); y > 0 {
		res.ModelYear = strconv.Itoa(y)
	}

	// 3. Pattern-based attributes.
	// Schemas are tried in ascending schema_id order (pre-sorted at load time).
	// The first schema with any matching pattern wins; remaining schemas skipped.
	for _, schema := range patternTable[wmi] {
		matched := false
		for _, p := range schema.patterns {
			if p.re.MatchString(key) {
				res.Attributes[p.variable] = p.value
				matched = true
			}
		}
		if matched {
			break
		}
	}

	return res, nil
}
