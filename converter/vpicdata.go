package main

// VPICData is the serialised form of the NHTSA VPIC lookup tables.
// The API binary embeds this as vpic.gob.gz.
//
// Structs must stay in sync with api/vpicdata.go. Gob uses exported field
// names for encoding so both sides just need matching names — package paths
// do not matter.
type VPICData struct {
	WMI      map[string]WMIEntry
	Patterns map[string][]SchemaGroup // WMI → schemas ordered by schema_id asc
}

// WMIEntry holds per-WMI Make/Manufacturer/VehicleType data.
type WMIEntry struct {
	MakeNames   string // comma-separated; primary (lowest ID) make is first
	MfrName     string
	VehicleType string
}

// SchemaGroup is one VIN schema's worth of patterns for a WMI.
// Schemas are tried in ascending schema_id order; the first schema with any
// matching pattern wins and no further schemas are evaluated.
type SchemaGroup struct {
	SchemaID int
	Patterns []PatternEntry // ordered by pattern_id asc
}

// PatternEntry is one regex→value mapping within a schema.
type PatternEntry struct {
	Regex    string
	Variable string
	Value    string
}
