-- Widen display_name from VARCHAR(64) to VARCHAR(128).
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    display_name VARCHAR(128),
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email),
    KEY users_display_name_idx (display_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE posts (
    id BIGINT NOT NULL AUTO_INCREMENT,
    user_id BIGINT NOT NULL,
    title VARCHAR(128) NOT NULL,
    PRIMARY KEY (id),
    KEY posts_user_id_idx (user_id),
    CONSTRAINT posts_user_id_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
