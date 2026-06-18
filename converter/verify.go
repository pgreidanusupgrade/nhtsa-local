package main

// verifyProcedureIntegrity runs a spot-check after conversion.
//
// It decodes a small set of well-known VINs via two independent paths:
//
//  1. vpic.spVinDecode — the authoritative Postgres stored procedure
//  2. Raw-table query — the exact JOIN the converter uses to populate SQLite
//
// If Make or ModelYear differ between the two paths, the stored-procedure logic
// has changed in a way the converter does not replicate. The build is aborted.
//
// WHAT THIS DOES NOT CATCH
// Fields that spVinDecode derives through extra procedure logic (ErrorCode,
// PlantCity, AdditionalErrorText, etc.) are not in our SQLite and are not
// checked here. Only Make and ModelYear are asserted because both flow
// through the normal pattern-matched element-value path.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// probeVINs are stable real-world VINs used for the integrity check.
// Their Make and ModelYear cannot change — they were already manufactured.
var probeVINs = []struct {
	vin      string
	wantMake string // expected substring (case-insensitive) in Make
	wantYear string // exact ModelYear value
}{
	{"1HGCM82633A004352", "honda", "2003"},
	{"1FTFW1ET5EKE52261", "ford", "2014"},
	{"2T1BURHE0JC060752", "toyota", "2018"},
}

func verifyProcedureIntegrity(ctx context.Context, conn *pgx.Conn) error {
	for _, probe := range probeVINs {
		spMake, spYear, err := decodeViaStoredProc(ctx, conn, probe.vin)
		if err != nil {
			return fmt.Errorf("spVinDecode(%s): %w", probe.vin, err)
		}
		if spMake == "" || spYear == "" {
			return fmt.Errorf("spVinDecode(%s): empty Make=%q or ModelYear=%q — DB may not be fully loaded", probe.vin, spMake, spYear)
		}

		rawMake, rawYear, err := decodeViaRawTables(ctx, conn, probe.vin)
		if err != nil {
			return fmt.Errorf("raw-table decode(%s): %w", probe.vin, err)
		}

		if !strings.EqualFold(spMake, rawMake) {
			return fmt.Errorf("VIN %s Make mismatch:\n  spVinDecode   = %q\n  raw-tables    = %q\n"+
				"The stored procedure may derive Make through a different path than element-value lookup.", probe.vin, spMake, rawMake)
		}
		if spYear != rawYear {
			return fmt.Errorf("VIN %s ModelYear mismatch:\n  spVinDecode = %q\n  raw-tables  = %q\n"+
				"Check whether the year-element mapping has changed.", probe.vin, spYear, rawYear)
		}

		// Sanity-check against our hardcoded expectations.
		if !strings.Contains(strings.ToLower(spMake), probe.wantMake) {
			return fmt.Errorf("VIN %s: Make=%q does not contain expected %q\n"+
				"Update probeVINs in verify.go if the NHTSA manufacturer name changed.", probe.vin, spMake, probe.wantMake)
		}
		if spYear != probe.wantYear {
			return fmt.Errorf("VIN %s: ModelYear=%q does not match expected %q\n"+
				"ModelYear for an already-manufactured vehicle cannot change — investigate.", probe.vin, spYear, probe.wantYear)
		}

		fmt.Printf("  ✓ %s  Make=%q  ModelYear=%q\n", probe.vin, spMake, spYear)
	}
	return nil
}

// decodeViaStoredProc calls vpic.spVinDecode and returns Make + ModelYear.
func decodeViaStoredProc(ctx context.Context, conn *pgx.Conn, vin string) (make_, year string, err error) {
	rows, err := conn.Query(ctx, `
		SELECT variable, value
		FROM vpic.spVinDecode($1)
		WHERE variable IN ('Make', 'Model Year')
		  AND value IS NOT NULL AND value != ''
	`, vin)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			return "", "", err
		}
		switch variable {
		case "Make":
			make_ = value
		case "Model Year":
			year = value
		}
	}
	return make_, year, rows.Err()
}

// decodeViaRawTables replicates the converter's JOIN query for a single VIN.
// This is the same logic that ends up in SQLite — if it diverges from
// spVinDecode the converter needs updating.
func decodeViaRawTables(ctx context.Context, conn *pgx.Conn, vin string) (make_, year string, err error) {
	if len(vin) < 10 {
		return "", "", fmt.Errorf("VIN too short")
	}
	wmi := strings.ToUpper(vin[:6])
	// Key string: VDS (positions 4-8) + "|" + VIS start (positions 10-17).
	// Source: vpic.spvindecode_core — var_keys = SUBSTRING(var_vin,4,5) || '|' || SUBSTRING(var_vin,10,8)
	key := strings.ToUpper(vin[3:8] + "|" + vin[9:])

	rows, err := conn.Query(ctx, `
		SELECT
		    vpic.sqlwild_to_regex(p.keys) AS regex,
		    e.name                        AS variable,
		    COALESCE(
		        vpic.felementattributevalue(ev.elementId, ev.attributeId),
		        ev.textvalue,
		        ''
		    ) AS value
		FROM vpic.pattern p
		JOIN vpic.wmi w           ON w.id = p.wmiId
		JOIN vpic.elementvalue ev  ON ev.vinschemaId = p.vinschemaId
		JOIN vpic.element e        ON e.id = ev.elementId
		WHERE w.wmi = $1
		  AND e.name IN ('Make', 'Model Year')
		  AND COALESCE(
		        vpic.felementattributevalue(ev.elementId, ev.attributeId),
		        ev.textvalue, ''
		      ) != ''
		ORDER BY p.id
	`, wmi)
	if err != nil {
		return "", "", fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var regexStr, variable, value string
		if err := rows.Scan(&regexStr, &variable, &value); err != nil {
			return "", "", err
		}
		re, err := regexp.Compile("(?i)" + regexStr)
		if err != nil || !re.MatchString(key) {
			continue
		}
		switch variable {
		case "Make":
			if make_ == "" {
				make_ = value
			}
		case "Model Year":
			if year == "" {
				year = value
			}
		}
	}
	return make_, year, rows.Err()
}
