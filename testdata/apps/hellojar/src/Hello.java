import com.sun.net.httpserver.HttpServer;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;

// Tiny HTTP server used as the "prebuilt JAR" fixture for DeployMate's
// artifact-deploy e2e (docs/specs/prebuilt-deploys.md). Honors the platform
// convention: listens on -Dserver.port (the wrapper passes ${PORT}) and
// answers every path with a fixed marker the e2e greps for.
public class Hello {
    public static void main(String[] args) throws Exception {
        int port = Integer.getInteger("server.port", 8080);
        HttpServer srv = HttpServer.create(new InetSocketAddress(port), 0);
        srv.createContext("/", ex -> {
            byte[] body = "deploymate e2e prebuilt jar fixture\n".getBytes(StandardCharsets.UTF_8);
            ex.sendResponseHeaders(200, body.length);
            ex.getResponseBody().write(body);
            ex.close();
        });
        srv.start();
        System.out.println("hello fixture listening on " + port);
    }
}
