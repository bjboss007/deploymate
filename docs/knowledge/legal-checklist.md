# Legal checklist for selling managed DeployMate

Written 2026-10-17. **Not legal advice**: a list of the questions to put to a lawyer who knows the owner's country and
the customers' (UK/EU/Nigeria/US differ), and what the code can already do to make the answers easier. Order is by
"must settle before the first paid customer".

## Before the first paid customer
1. **Who is the seller.** A registered company (not a person) should sign contracts: limits personal liability and
   is what business customers expect. Tax registration and VAT/sales-tax treatment of software plus a managed service
   follow from where the company and the customers are.
2. **Customer terms (an MSA or terms of service).** Scope of "managed" (what is monitored, what is not), no data-loss
   guarantee beyond the backups the customer enabled, uptime target or none (an SLA is a promise: only write one you
   can keep), liability cap (usually fees paid in 12 months), exclusion of indirect loss, term and cancellation,
   what happens to the server when the customer stops paying. B2B only keeps this much simpler than consumer law.
3. **Personal data: who is controller and who is processor.** The customer's apps hold their users' data; once we can
   log in to their server we are a **processor**. That needs a **data processing agreement** (GDPR/UK GDPR, or the
   local equivalent such as Nigeria's NDPA), a list of **sub-processors** (Cloudflare, the VPS provider, the backup
   storage, any monitoring), and a breach-notification promise (typically 24-72 h to the customer).
4. **What access we have and when we use it.** The visible, revocable SSH key and the management token are the
   mechanism; the contract should say we only use them to deliver the service or on the customer's request, and that
   we log it. This is the single most important trust statement.
5. **Third-party software licences.** Check what we install or let customers install:
   - **MongoDB** is under the **SSPL**, which restricts offering MongoDB "as a service" to third parties. Customers
     running their own instance on their own server under their own pull of the image is the safe shape; us hosting
     MongoDB for them on our servers is the risky one. Ask the lawyer before ever hosting it ourselves.
   - **Redis** changed licences after 7.2 (source-available, then AGPL again from 8); `redis:7-alpine` may now be a
     source-available version. The same "as a service" question applies. Valkey is the permissive fork if it matters.
   - Docker Engine, Traefik, cloudflared, Railpack, BuildKit, Postgres, MySQL (GPL; fine to run, not to redistribute
     modified) have their own terms; the repo ships none of them, which keeps redistribution out of scope.
   - **Cloudflare**: using a tunnel is fine; reselling Cloudflare services has its own terms. The tunnel living in the
     customer's account (decision 2) avoids that.
6. **Acceptable use and abuse handling.** Customers will host things we do not control. Terms need an acceptable-use
   clause and a process for takedown or abuse reports, even if the server is theirs.

## Soon after
7. **Trademark and name.** Search "DeployMate" (it is a common name) in the markets that matter before putting money
   behind it; register the name or logo if it clears. The Apache-2.0 licence does not grant trademark rights, which is
   good: forks must rename.
8. **Open-source licence strategy.** Released versions (v0.1.0 to date) are Apache-2.0 forever. If competitors reselling
   DeployMate is a worry, a later change to AGPL or the Business Source License applies only to future versions and
   needs the right to relicense: every outside contribution should come with a **CLA or DCO sign-off** from now on,
   otherwise old contributors can block a licence change. Today the repo has contributions from the owner only.
9. **Privacy policy and cookies** for the website and, if added, a sign-up flow. The dashboard itself sets only a
   session cookie and sends no telemetry (verify before saying so in public).
10. **Security promises.** Anything written on the website ("encrypted backups", "hardened by default") is a claim
    that can be held against us. Keep the wording to what the code does; `docs/specs/server-security.md` is the
    source of truth.
11. **Insurance** (professional indemnity / cyber) once customers' production runs on servers we manage.
12. **Export and sanctions** screening of customers, and payment terms (refunds, chargebacks, currency).

## What the code and docs can do for this
- A public **security and data handling page** generated from the specs (what we can access, what leaves the
  server, what is encrypted, where backups go).
- A **sub-processor list** kept in the repo, updated whenever a provider is added.
- A **NOTICE / third-party licences file** in the release archives.
- The support key and management token shown, labelled and revocable on the Security and Tokens pages.
- DCO sign-off checks in CI for outside contributions.

## Questions to take to the lawyer
Which country and entity type; DPA wording and sub-processor change process; liability cap and SLA wording; whether
hosting customer-chosen MongoDB/Redis images is acceptable under SSPL/RSAL; whether to relicense future versions and how
to collect contributor rights now; trademark clearance for "DeployMate".
