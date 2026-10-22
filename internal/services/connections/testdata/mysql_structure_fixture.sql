-- TEST-ONLY fixture. Import exclusively into the named ephemeral MySQL 8.4 container.
-- This file must NEVER be run against a production or shared database.
SET NAMES utf8mb4;
CREATE DATABASE IF NOT EXISTS nyadb_struct_a CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE IF NOT EXISTS nyadb_struct_b CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE nyadb_struct_a.parent (
    tenant_id INT NOT NULL,
    code VARCHAR(12) NOT NULL,
    label VARCHAR(60) NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, code)
) ENGINE=InnoDB COMMENT='NyaDB fixture parent';

CREATE TABLE nyadb_struct_a.child (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id INT NOT NULL,
    parent_code VARCHAR(12) NOT NULL,
    price DECIMAL(10,2) NOT NULL DEFAULT 0.00,
    nullable_note VARCHAR(80) DEFAULT NULL,
    blank_note VARCHAR(32) NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    remark TEXT COMMENT '仅供隔离测试',
    PRIMARY KEY (id),
    UNIQUE KEY uk_tenant_code (tenant_id, parent_code),
    KEY ix_note_prefix (nullable_note(12)),
    CONSTRAINT fk_child_parent FOREIGN KEY (tenant_id, parent_code)
        REFERENCES nyadb_struct_a.parent(tenant_id, code)
        ON UPDATE CASCADE ON DELETE RESTRICT
) ENGINE=InnoDB COMMENT='NyaDB fixture child';

CREATE VIEW nyadb_struct_a.parent_view AS
    SELECT tenant_id, code, label FROM nyadb_struct_a.parent;

CREATE TABLE nyadb_struct_b.child (
    id INT NOT NULL PRIMARY KEY,
    message VARCHAR(120) DEFAULT 'other schema'
) ENGINE=InnoDB;
