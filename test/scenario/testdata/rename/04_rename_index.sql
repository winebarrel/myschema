-- Rename two indexes in a single step via inline directives:
--   * users_legacy_email_key → users_email_key (UNIQUE)
--   * old_display_name_idx   → users_display_name_idx (KEY)
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    display_name VARCHAR(64),
    PRIMARY KEY (id),
    -- myschema:renamed-from users_legacy_email_key
    UNIQUE KEY users_email_key (email),
    -- myschema:renamed-from old_display_name_idx
    KEY users_display_name_idx (display_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
