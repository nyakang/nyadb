-- Disposable NyaDB MySQL 8.4 fixture only. Never import into production.
SET NAMES utf8mb4;
CREATE DATABASE IF NOT EXISTS nyadb_types CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE TABLE nyadb_types.field_types (
  id BIGINT NOT NULL PRIMARY KEY,
  status ENUM('active','disabled','待审核') NOT NULL DEFAULT 'active' COMMENT '状态枚举',
  attributes JSON,
  description VARCHAR(255) DEFAULT 'NULL'
) ENGINE=InnoDB COMMENT='NyaDB isolated ENUM/JSON metadata';
INSERT INTO nyadb_types.field_types (id, status, attributes, description) VALUES
  (1, 'active', JSON_OBJECT('tag','ok','count',3), 'normal'),
  (2, 'disabled', JSON_OBJECT('payload',REPEAT('中',4096)), 'NULL'),
  (3, '待审核', NULL, NULL);

-- MYSQL_USER/MYSQL_PASSWORD_FILE provision this disposable account in
-- the container entrypoint. Drop its bootstrap ALL grant and leave SELECT only.
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'nyadb_it_reader'@'%';
GRANT SELECT, SHOW VIEW ON nyadb_types.* TO 'nyadb_it_reader'@'%';
GRANT SELECT, SHOW VIEW ON nyadb_struct_a.* TO 'nyadb_it_reader'@'%';
GRANT SELECT, SHOW VIEW ON nyadb_struct_b.* TO 'nyadb_it_reader'@'%';
