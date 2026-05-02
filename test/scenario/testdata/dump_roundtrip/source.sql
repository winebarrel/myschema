-- Source schema for the dump round-trip scenario. Exercises the most
-- common features so the dump output covers each renderer branch.
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    display_name VARCHAR(64),
    PRIMARY KEY (id),
    UNIQUE KEY users_email_key (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE posts (
    id BIGINT NOT NULL AUTO_INCREMENT,
    user_id BIGINT NOT NULL,
    title VARCHAR(128) NOT NULL,
    body TEXT,
    PRIMARY KEY (id),
    KEY posts_user_id_idx (user_id),
    CONSTRAINT posts_user_id_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE VIEW active_users AS
    SELECT id, email FROM users WHERE display_name IS NOT NULL;
