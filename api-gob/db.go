package main

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"regexp"
)

// compiledPattern is a PatternEntry with its regex pre-compiled.
type compiledPattern struct {
	re       *regexp.Regexp
	variable string
	value    string
}

// compiledSchema is a SchemaGroup with compiled patterns.
type compiledSchema struct {
	schemaID int
	patterns []compiledPattern
}

// Global lookup tables populated once at startup by loadVPICData.
var (
	wmiTable     map[string]WMIEntry
	patternTable map[string][]compiledSchema // WMI → schemas ordered by schema_id asc
)

// loadVPICData deserialises the embedded vpic.gob.gz and pre-compiles all
// regex patterns. Called once from main before serving requests.
func loadVPICData() error {
	if len(vpicGobGz) == 0 {
		return fmt.Errorf("vpic.gob.gz is empty — run 'make convert' then rebuild")
	}

	gr, err := gzip.NewReader(bytes.NewReader(vpicGobGz))
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	var data VPICData
	if err := gob.NewDecoder(gr).Decode(&data); err != nil {
		return fmt.Errorf("gob decode: %w", err)
	}

	// Validate basic sanity — catches an empty/truncated embed.
	if len(data.WMI) < 1000 {
		return fmt.Errorf("vpic.gob.gz appears truncated: only %d WMI entries", len(data.WMI))
	}

	wmiTable = data.WMI

	patternTable = make(map[string][]compiledSchema, len(data.Patterns))
	var failedRegex int
	for wmi, schemas := range data.Patterns {
		compiled := make([]compiledSchema, 0, len(schemas))
		for _, sg := range schemas {
			cs := compiledSchema{schemaID: sg.SchemaID}
			for _, p := range sg.Patterns {
				re, err := regexp.Compile("(?i)" + p.Regex)
				if err != nil {
					failedRegex++
					continue
				}
				cs.patterns = append(cs.patterns, compiledPattern{
					re:       re,
					variable: p.Variable,
					value:    p.Value,
				})
			}
			if len(cs.patterns) > 0 {
				compiled = append(compiled, cs)
			}
		}
		patternTable[wmi] = compiled
	}

	if failedRegex > 0 {
		return fmt.Errorf("%d patterns failed to compile — vpic.gob.gz may be corrupt", failedRegex)
	}
	return nil
}
