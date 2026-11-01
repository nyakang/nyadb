package dataworkspace

import mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"

func MySQLRuntime(m *mysqlsvc.MySQLSessionManager) Runtime { return mysqlsvc.DataRuntime(m) }
