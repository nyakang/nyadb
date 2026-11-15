import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.DriverManager;

// Creates only databases owned by the driver acceptance fixture. Passwords
// stay in the private file and JDBC calls, never in process arguments or logs.
public final class H2DriverFiles {
    private static void create(Path base, String options, String password) throws Exception {
        Files.createDirectories(base.getParent());
        try (var connection = DriverManager.getConnection("jdbc:h2:file:" + base + options, "sa", password);
             var statement = connection.createStatement()) {
            statement.execute("CREATE TABLE \"driver_records\"(\"id\" BIGINT PRIMARY KEY,\"note\" VARCHAR)");
            statement.execute("INSERT INTO \"driver_records\" VALUES(9007199254740993,'original driver file')");
        }
    }
    public static void main(String[] args) throws Exception {
        Path root = Path.of(args[0]);
        String password = Files.readString(Path.of(args[1])).trim();
        for (String role : new String[]{"provider-auto", "provider-custom", "shell-auto", "shell-custom"}) {
            create(root.resolve(role).resolve(args[2]), "", password);
        }
        if (args[2].equals("h2-v1")) create(root.resolve("provider-auto/page-store"), ";MV_STORE=FALSE", password);
        if (args[2].equals("h2-v3")) create(root.resolve("provider-auto/encrypted"), ";CIPHER=AES", password + " " + password);
        System.out.println("PASS owned native driver files " + args[2]);
    }
}
