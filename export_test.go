package myschema

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var SkipIdentifier = skipIdentifier
