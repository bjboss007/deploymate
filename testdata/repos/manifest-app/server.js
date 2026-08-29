// Fixture: proves the infra manifest works end to end — the repo declares
// Postgres in deploymate.yml, DeployMate auto-provisions it and injects
// DATABASE_URL, and this app queries a database it never configured itself.
const http = require("http");
const { Client } = require("pg");
const port = process.env.PORT || 3000;

http
  .createServer(async (req, res) => {
    if (!process.env.DATABASE_URL) {
      res.end("no DATABASE_URL injected");
      return;
    }
    const client = new Client({ connectionString: process.env.DATABASE_URL });
    try {
      await client.connect();
      const r = await client.query("SELECT 1 AS ok");
      res.end("postgres says ok: " + r.rows[0].ok);
    } catch (e) {
      res.end("postgres error: " + e.message);
    } finally {
      await client.end().catch(() => {});
    }
  })
  .listen(port, () => console.log("manifest-app listening on " + port));
