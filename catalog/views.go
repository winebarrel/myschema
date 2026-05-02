package catalog

import (
	"context"
	"fmt"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// Views loads every view in the configured database.
func (c *Catalog) Views(ctx context.Context) (*orderedmap.Map[string, *model.View], error) {
	out := orderedmap.New[string, *model.View]()
	q := `
SELECT TABLE_SCHEMA, TABLE_NAME, VIEW_DEFINITION, CHECK_OPTION,
       DEFINER, SECURITY_TYPE
FROM   information_schema.VIEWS
WHERE  TABLE_SCHEMA = ?
ORDER  BY TABLE_NAME`

	rows, err := c.conn.QueryContext(ctx, q, c.database)
	if err != nil {
		return nil, fmt.Errorf("catalog: list views: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	for rows.Next() {
		var (
			db, name, def     string
			checkOption       string
			definer, security string
		)
		if err := rows.Scan(&db, &name, &def, &checkOption, &definer, &security); err != nil {
			return nil, fmt.Errorf("catalog: scan views: %w", err)
		}
		v := &model.View{
			Database:    db,
			Name:        name,
			Definition:  def,
			CheckOption: checkOption,
			Definer:     definer,
			Security:    security,
		}
		out.Set(model.Ident(db, name), v)
	}
	return out, rows.Err()
}
