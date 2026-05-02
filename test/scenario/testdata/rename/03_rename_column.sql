-- Rename users.legacy_email → users.email (inline directive above
-- the column line).
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    -- myschema:renamed-from legacy_email
    email VARCHAR(255) NOT NULL,
    display_name VARCHAR(64),
    PRIMARY KEY (id),
    UNIQUE KEY users_legacy_email_key (email),
    KEY old_display_name_idx (display_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
