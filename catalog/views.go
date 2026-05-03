package catalog

import (
	"context"
	"fmt"

	"github.com/winebarrel/myschema/model"
	"github.com/winebarrel/orderedmap"
)

// Views loads every view in the configured database.
func (c *Catalog) Views(ctx context.Context) (*orderedmap.Map[string, *model.View], error) {
	if err := c.ping(ctx); err != nil {
		return nil, err
	}
	out := orderedmap.New[string, *model.View]()
	// DEFINER and SECURITY_TYPE are intentionally not selected — they
	// are out of scope for v1 (see CAVEATS.md). Adding them back means
	// also threading them through model.View, parser/view.go, and
	// diff/views.go.
	q := `
SELECT TABLE_SCHEMA, TABLE_NAME, VIEW_DEFINITION, CHECK_OPTION
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
			db, name, def string
			checkOption   string
		)
		if err := rows.Scan(&db, &name, &def, &checkOption); err != nil {
			return nil, fmt.Errorf("catalog: scan views: %w", err)
		}
		v := &model.View{
			Database:    db,
			Name:        name,
			Definition:  def,
			CheckOption: checkOption,
		}
		out.Set(model.Ident(db, name), v)
	}
	return out, rows.Err()
}
