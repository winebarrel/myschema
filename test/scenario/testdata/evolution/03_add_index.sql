CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    display_name VARCHAR(64) NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email),
    KEY users_display_name_idx (display_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
