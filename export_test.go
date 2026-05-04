package myschema

// Re-export unexported helpers for external test packages so behaviour
// can be pinned without polluting the public API.

var (
	SkipIdentifier        = skipIdentifier
	AppendBeforeSemicolon = appendBeforeSemicolon
	PartitionOpInsertPos  = partitionOpInsertPos
)

// DSNConfigFor invokes the unexported dsnConfig method on Client and
// surfaces the parsed db-name and (post-override) password for
// assertion. Returns a non-nil error when dsnConfig fails (malformed
// DSN, missing database name, etc.); both string returns are zero
// in that case.
func DSNConfigFor(c *Client) (dbName, password string, err error) {
	cfg, err := c.dsnConfig()
	if err != nil {
		return "", "", err
	}
	return cfg.DBName, cfg.Passwd, nil
}
