// No-Dockerfile fixture: proves the runtime selection path builds and
// runs a plain Node.js app via Railpack.
const http = require("http");
const port = process.env.PORT || 3000;

http
  .createServer((req, res) => {
    res.end("node says: hello from " + process.version);
  })
  .listen(port, () => console.log("node-app listening on " + port));
