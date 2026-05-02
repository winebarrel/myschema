-- Desired declares only `users`. Without filtering, plan would also
-- emit DROP TABLE for sessions and logs.
CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
