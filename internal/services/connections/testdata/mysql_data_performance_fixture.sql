-- NyaDB isolated disposable performance fixtures. NEVER apply to production.
SET NAMES utf8mb4;
CREATE DATABASE IF NOT EXISTS nyadb_perf CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
USE nyadb_perf;
CREATE TABLE no_key_entries (
    note VARCHAR(64) NOT NULL,
    score INT NOT NULL
) ENGINE=InnoDB;
CREATE TABLE unique_no_primary (
    code VARCHAR(32) NOT NULL UNIQUE,
    score INT NOT NULL
) ENGINE=InnoDB;
CREATE TABLE blob_entries (
    id INT NOT NULL PRIMARY KEY,
    payload LONGBLOB,
    description LONGTEXT
) ENGINE=InnoDB;
INSERT INTO no_key_entries (note, score) VALUES
    ('z',3),('a',1),('b',2),('c',4);
-- Ten thousand rows for bounded offset/pagination checks.
SET SESSION cte_max_recursion_depth=11000;
INSERT INTO no_key_entries(note,score)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 10000)
SELECT CONCAT('perf_',LPAD(n,5,'0')),n FROM seq;
INSERT INTO unique_no_primary(code,score) VALUES
    ('B',5),('A',4),('C',6);
INSERT INTO blob_entries (id,payload,description) VALUES
    (1, REPEAT(UNHEX('AB'), 1048576), REPEAT('中', 400000)),
    (2, NULL, '');
