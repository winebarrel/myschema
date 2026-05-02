-- Rename old_users → users (table-level directive on the leading line).
-- myschema:renamed-from old_users
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    legacy_email VARCHAR(255) NOT NULL,
    display_name VARCHAR(64),
    PRIMARY KEY (id),
    UNIQUE KEY users_legacy_email_key (legacy_email),
    KEY old_display_name_idx (display_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
