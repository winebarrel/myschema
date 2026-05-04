package myschema

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var (
	SkipIdentifier        = skipIdentifier
	AppendBeforeSemicolon = appendBeforeSemicolon
	PartitionOpInsertPos  = partitionOpInsertPos
)

// DSNConfigFor invokes the unexported dsnConfig method on Client.
// Returned bool reports whether the parse succeeded; on success the
// db-name and password are exposed for assertion.
func DSNConfigFor(c *Client) (dbName, password string, err error) {
	cfg, err := c.dsnConfig()
	if err != nil {
		return "", "", err
	}
	return cfg.DBName, cfg.Passwd, nil
}
