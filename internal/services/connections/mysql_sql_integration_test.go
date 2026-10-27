package connections

import (
    "context"
    "errors"
    "os"
    "strings"
    "testing"
    "time"
)

// Opt-in test only for an isolated mysql:8.4 container published on loopback
// port 34307 with the dedicated nyadb_sql_it schema. Never use production.
func TestMySQLExecuteSQLIntegrationMySQL84(t *testing.T) {
    if os.Getenv("NYADB_MYSQL_SQL_IT")!="1" {
        t.Skip("requires disposable mysql:8.4 fixture on 127.0.0.1:34307")
    }
    password:=mysqlIntegrationPassword(t)
    manager:=NewMySQLSessionManager()
    t.Cleanup(func(){_ = manager.Close()})
    settings:=ActiveSettings{
        ProfileID:84307, ConnectionName:"disposable-sql-it",
        Host:"127.0.0.1",Port:34307,Username:"root",
        MySQLAdvanced:DefaultMySQLAdvanced(),
    }
    ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
    defer cancel()
    if err:=manager.Connect(ctx,settings,password);err!=nil {t.Fatalf("connect to disposable fixture: %v",err)}
    database:="nyadb_sql_it"

    // Always use a dedicated test schema/table. No existing schema is modified.
    if _,err:=manager.ExecuteSQL(ctx,settings.ProfileID,database,
        "CREATE TABLE IF NOT EXISTS query_fixture (id INT PRIMARY KEY, value VARCHAR(80))");err!=nil {t.Fatal(err)}
    if _,err:=manager.ExecuteSQL(ctx,settings.ProfileID,database,"DELETE FROM query_fixture");err!=nil {t.Fatal(err)}
    add,err:=manager.ExecuteSQL(ctx,settings.ProfileID,database,
        "INSERT INTO query_fixture (id,value) VALUES (1,'alpha'),(2,'beta')")
    if err!=nil || add.AffectedRows!=2 {t.Fatalf("insert: %+v %v",add,err)}
    rows,err:=manager.ExecuteSQL(ctx,settings.ProfileID,database,
        "SELECT id,value FROM query_fixture ORDER BY id")
    if err!=nil || len(rows.Columns)!=2 || len(rows.Rows)!=2 ||
        rows.Rows[0][1]!="alpha" || rows.Rows[1][1]!="beta" {
        t.Fatalf("SELECT results: %+v %v",rows,err)
    }
    update,err:=manager.ExecuteSQL(ctx,settings.ProfileID,database,
        "UPDATE query_fixture SET value='gamma' WHERE id=2")
    if err!=nil || update.AffectedRows!=1 {t.Fatalf("update: %+v %v",update,err)}
    selected,err:=manager.ExecuteSQL(ctx,settings.ProfileID,database,"SELECT DATABASE()")
    if err!=nil || len(selected.Rows)!=1 || selected.Rows[0][0]!=database {
        t.Fatalf("schema leakage: %+v %v",selected,err)
    }
    if _,err:=manager.ExecuteSQL(ctx,settings.ProfileID,database,"SELECT missing_column FROM query_fixture");
        err==nil || !strings.Contains(strings.ToLower(err.Error()),"unknown column") {
        t.Fatalf("expected server syntax/column error, got %v",err)
    }
    cancelCtx,stop:=context.WithTimeout(ctx,150*time.Millisecond)
    defer stop()
    if _,err:=manager.ExecuteSQL(cancelCtx,settings.ProfileID,database,"SELECT SLEEP(10)");
        !errors.Is(err,context.DeadlineExceeded) && !errors.Is(err,context.Canceled) {
        t.Fatalf("real query did not honor cancellation: %v",err)
    }
    recovered,err:=manager.ExecuteSQL(ctx,settings.ProfileID,database,"SELECT COUNT(*) FROM query_fixture")
    if err!=nil || len(recovered.Rows)!=1 || recovered.Rows[0][0]!="2" {
        t.Fatalf("session unusable after cancel: %+v %v",recovered,err)
    }
}
