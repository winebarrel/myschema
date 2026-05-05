Output demo.gif

Set Shell "bash"
Set FontSize 12
Set Width 800
Set Height 600
Set Padding 20

Hide
Type "alias colorize='bat -l sql -p --paging=never' && clear"
Show

# Create schema file
Enter
Type "# Create a desired schema definition file" Enter
Sleep 2s
Type "cat <<'EOF' > schema.sql" Enter
Type@3ms "CREATE TABLE posts (" Enter
Type@3ms "    id INT NOT NULL AUTO_INCREMENT," Enter
Type@3ms "    user_id INT NOT NULL," Enter
Type@3ms "    title TEXT NOT NULL," Enter
Type@3ms "    PRIMARY KEY (id)," Enter
Type@3ms "    KEY idx_posts_user_id (user_id)" Enter
Type@3ms ");" Enter
Type@3ms "EOF" Enter
Sleep 0.5s
Enter
Type "colorize schema.sql" Enter
Sleep 4s

# Plan
Enter
Type "# Preview the changes to be applied (like terraform plan)" Enter
Sleep 2s
Type "myschema plan schema.sql | colorize" Enter
Sleep 4s

# Apply
Enter
Type "# Apply the schema to the database" Enter
Sleep 2s
Type "myschema apply schema.sql | colorize" Enter
Sleep 4s

# Verify no remaining diff
Enter
Type "# Verify that the schema is now in sync" Enter
Sleep 2s
Type "myschema plan schema.sql | colorize" Enter
Sleep 4s

# Dump
Enter
Type "# Dump the current database schema" Enter
Sleep 2s
Type "myschema dump | colorize" Enter
Sleep 4s

# Backup and modify schema: add a column
Enter
Type "# Modify the schema file: add a body column to posts" Enter
Sleep 2s
Type "cp schema.sql schema.sql.bak" Enter
Sleep 0.5s
Type "sed -i '' 's/title TEXT NOT NULL,/title TEXT NOT NULL,\n    body TEXT NOT NULL,/' schema.sql" Enter
Sleep 0.5s

# Show diff
Enter
Type "# Show the schema file diff" Enter
Sleep 2s
Type "diff -u --color schema.sql.bak schema.sql" Enter
Sleep 4s

# Plan the diff
Enter
Type "# Plan again to see the incremental changes" Enter
Sleep 2s
Type "myschema plan schema.sql | colorize" Enter
Sleep 4s

# Apply the diff
Enter
Type "# Apply the incremental changes" Enter
Sleep 2s
Type "myschema apply schema.sql | colorize" Enter
Sleep 4s

# Verify no remaining diff
Enter
Type "# Verify that the schema is now in sync" Enter
Sleep 2s
Type "myschema plan schema.sql | colorize" Enter
Sleep 4s

# Dump
Enter
Type "# Dump the current database schema" Enter
Sleep 2s
Type "myschema dump | colorize" Enter
Sleep 4s
