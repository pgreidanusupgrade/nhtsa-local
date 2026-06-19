package main

// VPICData is the deserialised form of the embedded vpic.gob.gz.
// Must stay in sync with converter/vpicdata.go — gob matches on exported
// field names, not package paths, so both sides just need identical names.
type VPICData struct {
	WMI      map[string]WMIEntry
	Patterns map[string][]SchemaGroup
}

// WMIEntry holds per-WMI Make/Manufacturer/VehicleType data.
type WMIEntry struct {
	MakeNames   string // comma-separated; primary (lowest ID) make is first
	MfrName     string
	VehicleType string
}

// SchemaGroup is one VIN schema's worth of patterns for a WMI.
type SchemaGroup struct {
	SchemaID int
	Patterns []PatternEntry
}

// PatternEntry is one regex→value mapping within a schema.
type PatternEntry struct {
	Regex    string
	Variable string
	Value    string
}
