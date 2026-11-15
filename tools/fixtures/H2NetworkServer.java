import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.DriverManager;
import org.h2.tools.Server;

// Owned verification fixture. Credentials are read from a private file.
public final class H2NetworkServer {
    public static void main(String[] args) throws Exception {
        String password = Files.readString(Path.of(args[1])).trim();
        System.setProperty("h2.bindAddress", "127.0.0.1");
        System.setProperty("javax.net.ssl.keyStore", args[2]);
        System.setProperty("javax.net.ssl.keyStorePassword", password);
        System.setProperty("javax.net.ssl.keyStoreType", "PKCS12");
        try (var connection = DriverManager.getConnection("jdbc:h2:file:" + args[0] + "/owned", "sa", password);
             var statement = connection.createStatement()) {
            statement.execute("CREATE TABLE \"network_records\"(\"id\" BIGINT PRIMARY KEY,\"note\" VARCHAR)");
            statement.execute("INSERT INTO \"network_records\" VALUES(9007199254740993,'native network original')");
        }
        Server tcp = Server.createTcpServer("-tcpPort", "0", "-baseDir", args[0]).start();
        Server ssl = Server.createTcpServer("-tcpPort", "0", "-tcpSSL", "-baseDir", args[0]).start();
        Runtime.getRuntime().addShutdownHook(new Thread(() -> { tcp.stop(); ssl.stop(); }));
        System.out.println("{\"TCPPort\":" + tcp.getPort() + ",\"SSLPort\":" + ssl.getPort() + "}");
        System.out.flush();
        System.in.read();
        tcp.stop();
        ssl.stop();
    }
}
