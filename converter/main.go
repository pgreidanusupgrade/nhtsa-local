// Converts the NHTSA VPIC PostgreSQL database into a flat SQLite file.
//
// Architecture:
//   - wmi table:      per-WMI lookup (Make, Manufacturer, VehicleType). WMI is
//                     the first 3 chars of a VIN (or 6 if position 3 is '9').
//   - patterns table: one row per (wmi × pattern × element). Contains the
//                     pre-converted regex and the resolved human-readable value.
//                     Elements 26/29/39 (Make/ModelYear/VehicleType) are absent
//                     from the pattern table — they come from the wmi table or
//                     are computed from the VIN year character at query time.
//
// Make comes from vpic.wmi_make → vpic.make (NOT from patterns).
// Model Year is derived from VIN position 10 at query time.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	_ "modernc.org/sqlite"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("converter failed: %v", err)
	}
}

// run wraps the conversion so deferred cleanup (db.Close) always executes.
// log.Fatalf calls os.Exit which skips deferred functions — if we exit via
// return instead, SQLite's WAL is checkpointed and the file is non-empty.
func run() error {
	pgURL := os.Getenv("DATABASE_URL")
	if pgURL == "" {
		pgURL = "postgres://vpic:vpic@localhost:5432/vpic?sslmode=disable"
	}
	outPath := os.Getenv("OUTPUT_PATH")
	if outPath == "" {
		outPath = "vpic.sqlite"
	}

	ctx := context.Background()

	log.Println("connecting to postgres...")
	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	log.Printf("opening sqlite at %s", outPath)
	db, err := sql.Open("sqlite", outPath)
	if err != nil {
		return fmt.Errorf("sqlite open: %w", err)
	}
	defer db.Close() // must run for WAL to checkpoint — do not use log.Fatalf below this line

	if err := createSchema(db); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	log.Println("exporting wmi lookup table...")
	if err := exportWMI(ctx, conn, db); err != nil {
		return fmt.Errorf("export wmi: %w", err)
	}

	log.Println("exporting pattern rules...")
	if err := exportPatterns(ctx, conn, db); err != nil {
		return fmt.Errorf("export patterns: %w", err)
	}

	log.Println("creating indexes...")
	if err := createIndexes(db); err != nil {
		return fmt.Errorf("indexes: %w", err)
	}

	log.Println("verifying procedure integrity...")
	if err := verifyProcedureIntegrity(ctx, conn, db); err != nil {
		return fmt.Errorf("PROCEDURE INTEGRITY CHECK FAILED: %w\n\n"+
			"The converter output does not match spVinDecode for probe VINs.\n"+
			"Do NOT ship this sqlite file.\n"+
			"See CLAUDE.md 'Procedure integrity' section for what to investigate.", err)
	}

	gobPath := strings.TrimSuffix(outPath, ".sqlite") + ".gob.gz"
	log.Printf("serialising gob.gz → %s", gobPath)
	if err := writeGobGz(db, gobPath); err != nil {
		return fmt.Errorf("write gob.gz: %w", err)
	}

	log.Println("done.")
	return nil
}

// writeGobGz reads the wmi and patterns tables from the just-written SQLite DB
// and serialises them as gob-encoded, gzip-compressed VPICData.
func writeGobGz(db *sql.DB, path string) error {
	data := VPICData{
		WMI:      make(map[string]WMIEntry),
		Patterns: make(map[string][]SchemaGroup),
	}

	// ── WMI table ──────────────────────────────────────────────────────────
	rows, err := db.Query(`SELECT wmi, make_names, mfr_name, vehicle_type FROM wmi`)
	if err != nil {
		return fmt.Errorf("query wmi: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var wmi string
		var makeNames, mfrName, vehicleType *string
		if err := rows.Scan(&wmi, &makeNames, &mfrName, &vehicleType); err != nil {
			return err
		}
		e := WMIEntry{}
		if makeNames != nil {
			e.MakeNames = *makeNames
		}
		if mfrName != nil {
			e.MfrName = *mfrName
		}
		if vehicleType != nil {
			e.VehicleType = *vehicleType
		}
		data.WMI[wmi] = e
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// ── Patterns table ─────────────────────────────────────────────────────
	// Fetch ordered so we can build SchemaGroups without an extra sort pass.
	prows, err := db.Query(`
		SELECT wmi, schema_id, pattern_id, regex, variable, value
		FROM patterns
		ORDER BY wmi, schema_id, pattern_id
	`)
	if err != nil {
		return fmt.Errorf("query patterns: %w", err)
	}
	defer prows.Close()

	// Temporary: map[wmi]map[schemaID][]PatternEntry for grouping.
	type schemaMap = map[int][]PatternEntry
	byWMI := map[string]schemaMap{}

	for prows.Next() {
		var wmi, regex, variable, value string
		var schemaID, patternID int
		if err := prows.Scan(&wmi, &schemaID, &patternID, &regex, &variable, &value); err != nil {
			return err
		}
		if byWMI[wmi] == nil {
			byWMI[wmi] = schemaMap{}
		}
		byWMI[wmi][schemaID] = append(byWMI[wmi][schemaID], PatternEntry{
			Regex:    regex,
			Variable: variable,
			Value:    value,
		})
	}
	if err := prows.Err(); err != nil {
		return err
	}

	for wmi, schemas := range byWMI {
		ids := make([]int, 0, len(schemas))
		for id := range schemas {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		groups := make([]SchemaGroup, 0, len(ids))
		for _, id := range ids {
			groups = append(groups, SchemaGroup{SchemaID: id, Patterns: schemas[id]})
		}
		data.Patterns[wmi] = groups
	}

	// ── Serialise ──────────────────────────────────────────────────────────
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(gw).Encode(data); err != nil {
		gw.Close()
		return fmt.Errorf("gob encode: %w", err)
	}
	if err := gw.Close(); err != nil {
		return err
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	log.Printf("  wrote %d WMI entries, %d WMIs with patterns, %.1f MB gzip'd",
		len(data.WMI), len(data.Patterns), float64(buf.Len())/1e6)
	return nil
}

func createSchema(db *sql.DB) error {
	// Each statement must be executed separately — modernc sqlite ignores all
	// but the first statement when multiple are passed to a single Exec call.
	stmts := []string{
		// Per-WMI Make/Manufacturer/VehicleType. A WMI may map to multiple makes
		// (stored comma-separated in make_names when ambiguous).
		`CREATE TABLE IF NOT EXISTS wmi (
			wmi              TEXT NOT NULL,
			make_id          INTEGER,
			make_names       TEXT,
			mfr_id           INTEGER,
			mfr_name         TEXT,
			vehicle_type_id  INTEGER,
			vehicle_type     TEXT,
			PRIMARY KEY (wmi)
		)`,
		// One row per (wmi × pattern × element). Regex already converted to Go format.
		// attribute_id mirrors pattern.attributeid which is varchar in postgres.
		`CREATE TABLE IF NOT EXISTS patterns (
			wmi          TEXT NOT NULL,
			pattern_id   INTEGER NOT NULL,
			schema_id    INTEGER NOT NULL,
			regex        TEXT NOT NULL,
			element_id   INTEGER NOT NULL,
			attribute_id TEXT,
			value        TEXT NOT NULL,
			variable     TEXT NOT NULL,
			PRIMARY KEY (wmi, pattern_id, element_id)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("exec %.40q: %w", s, err)
		}
	}
	return nil
}

// exportWMI writes one row per WMI with Make, Manufacturer, and VehicleType.
// A WMI can have multiple makes (e.g. shared WMI codes for trailers) — in
// that case make_id holds the lowest make ID and make_names is comma-joined.
func exportWMI(ctx context.Context, conn *pgx.Conn, db *sql.DB) error {
	rows, err := conn.Query(ctx, `
		SELECT
		    w.wmi,
		    (
		        SELECT mk.id FROM vpic.wmi_make wm2
		        JOIN vpic.make mk ON mk.id = wm2.makeid
		        WHERE wm2.wmiid = w.id ORDER BY mk.id LIMIT 1
		    ) AS make_id,
		    (
		        SELECT string_agg(mk.name, ',' ORDER BY mk.id)
		        FROM vpic.wmi_make wm2
		        JOIN vpic.make mk ON mk.id = wm2.makeid
		        WHERE wm2.wmiid = w.id
		    ) AS make_names,
		    w.manufacturerid,
		    mfr.name AS mfr_name,
		    w.vehicletypeid,
		    vt.name AS vehicle_type
		FROM vpic.wmi w
		LEFT JOIN vpic.manufacturer mfr ON mfr.id = w.manufacturerid
		LEFT JOIN vpic.vehicletype vt   ON vt.id  = w.vehicletypeid
		WHERE w.publicavailabilitydate <= NOW()
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO wmi(wmi, make_id, make_names, mfr_id, mfr_name, vehicle_type_id, vehicle_type)
		 VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for rows.Next() {
		var wmi string
		var makeID *int64
		var makeNames *string
		var mfrID *int
		var mfrName, vehicleType *string
		var vehicleTypeID *int

		if err := rows.Scan(&wmi, &makeID, &makeNames, &mfrID, &mfrName, &vehicleTypeID, &vehicleType); err != nil {
			tx.Rollback()
			return err
		}

		if _, err := stmt.Exec(wmi, makeID, makeNames, mfrID, mfrName, vehicleTypeID, vehicleType); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// exportPatterns writes one row per (wmi, pattern, element) combination.
// The regex is read from pattern.keys_regex (a stored generated column).
// felementattributevalue resolves the raw attributeid to a human-readable string.
//
// Elements 26 (Make), 29 (Model Year), 39 (Vehicle Type) have zero rows in
// the pattern table — they are derived from the wmi table or computed from
// the VIN year character, so they do not appear here.
func exportPatterns(ctx context.Context, conn *pgx.Conn, db *sql.DB) error {
	rows, err := conn.Query(ctx, `
		SELECT
		    w.wmi,
		    p.id                                                       AS pattern_id,
		    p.vinschemaid                                              AS schema_id,
		    p.keys_regex                                               AS regex,
		    p.elementid                                                AS element_id,
		    p.attributeid                                              AS attribute_id,
		    vpic.felementattributevalue(p.elementid, p.attributeid)    AS value,
		    e.name                                                     AS variable
		FROM vpic.pattern p
		JOIN vpic.wmi_vinschema wv ON wv.vinschemaid = p.vinschemaid
		JOIN vpic.wmi w            ON w.id = wv.wmiid
		JOIN vpic.element e        ON e.id = p.elementid
		WHERE w.publicavailabilitydate <= NOW()
		  AND p.attributeid != ''
		  AND vpic.felementattributevalue(p.elementid, p.attributeid) != ''
		  AND e.decode IS NOT NULL
		  AND coalesce(e.isprivate, false) = false
	`)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO patterns(wmi, pattern_id, schema_id, regex, element_id, attribute_id, value, variable)
		VALUES(?,?,?,?,?,?,?,?)
	`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	n := 0
	for rows.Next() {
		var wmi, regex, value, variable string
		var patternID, schemaID, elementID int
		var attributeID *string
		if err := rows.Scan(&wmi, &patternID, &schemaID, &regex, &elementID, &attributeID, &value, &variable); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := stmt.Exec(wmi, patternID, schemaID, regex, elementID, attributeID, value, variable); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert row %d: %w", n, err)
		}
		n++
		if n%100000 == 0 {
			log.Printf("  %d rows...", n)
		}
	}
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return err
	}
	log.Printf("  total: %d rows", n)
	return tx.Commit()
}

func createIndexes(db *sql.DB) error {
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_patterns_wmi ON patterns(wmi)`)
	return err
}
