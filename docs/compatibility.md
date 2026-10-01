# Experimental API and administration

## Registry provenance

`internal/social/operations.json` contains exact operation names and query IDs
from the pinned develop checkout of TwitterInternalAPIDocument and the saved
public X web bundle. There are 634 distinct name/ID pairs, not 634 distinct
features. 110 pairs are excluded: Grok, direct messages, Creator Studio/analytics,
and payments. Unknown IDs and excluded features return explicit errors.
`internal/social/operations.json` records the semantic handler and captured selections for each pair.

Routing an operation is not proof of full X compatibility. The documentation
contains many unknown request/response schemas. Captured Relay selections are
used where available; older response roots and several nested formats are
inferred. This is a local experimental implementation, not an upstream X API
proxy, and all 524 included pairs have **not** been individually verified in
the official web and mobile clients.

## Stored behavior

Bookmarks and folders, lists/members/subscriptions, communities/roles/rules,
follow requests for protected accounts, blocking/muting, user preferences,
notifications, pinned timelines and posts, articles, community notes and ratings,
drafts and scheduled posts are stored in Postgres. A worker publishes scheduled
posts using row locks. The existing tweet, profile, media and Telegram auth
services remain in use. Reports support dismissal, reversible post hiding,
account suspension and restoration. Verification changes and moderation are
audited. Hidden posts, suspended accounts, blocks and private profiles are
checked before rendering posts and performing likes/reposts.

In Telegram authentication mode, Premium is purchased in the Telegram bot.
In email mode, Telegram actions and purchases are disabled and Premium and
verification are granted manually in admin. No X checkout API is provided.

Sports, events, GIFs, Spaces and broadcast queries read the local catalog.
Records must be imported in the admin panel. This does not supply live NFL
feeds, audio conferencing, a Periscope broadcast infrastructure, machine
translation or Widevine licenses. The experimental media token is a local
signed playback token, not a Widevine license. Those external capabilities are
not complete simply because their operation is in the registry.

## Admin

HTTPS: `https://flashlarry.duckdns.org:2600`, internal service `127.0.0.1:8084`.
Username: `admin`. The generated password has 100 random characters including
letters, digits and punctuation. Its hash is in `/etc/flashlarry/admin.env`;
plaintext credentials are in `/root/flashlarry-admin-credentials.txt` and the
operator's private credentials file, permissions 0600.
Admin sessions expire after 8 hours; cookies are Secure/HttpOnly/SameSite Strict;
write requests require same-origin and CSRF checks. Login attempts are limited.
The social service is accessible only on loopback and requires an internal token.

The users tab also provides direct Suspend/Restore, manual Premium, account
email and recommendation priority (-100..100). Suspension rejects sessions,
password/OTP login and Telegram purchases, hides authored posts from feeds and
is reversible. Changes are recorded in the moderation audit.

The “Лента и вход” tab switches chronological/algorithmic Home ordering and
Telegram/real-email codes without a service restart. SMTP host, port, login,
sender, TLS mode and password are entered there. Passwords are AES-256-GCM
encrypted using `SETTINGS_ENCRYPTION_KEY` in `/etc/flashlarry/common.env` and
never returned to the browser. Back up that key together with the database.
Only TLS or STARTTLS with certificate verification is supported. Real-email
accounts are created only after the first successful code verification; existing
Telegram accounts can be assigned a real email in admin. OTPs expire after five
minutes, can be consumed once and are invalidated by authentication mode/SMTP
changes. Existing sessions are preserved across mode changes.

The bot continues polling to discard disabled-mode updates and reject pending
pre-checkout requests; it does not register, send codes or issue invoices while
email mode is active. A confirmed payment arriving after the mode switch is
stored once in `fl_pending_premium_payments` and creates an admin report for
manual Premium/badge fulfillment. It never silently discards a paid receipt.
The admin interface and OTP endpoints do not validate
SMTP deliverability until a real login attempt is made.

## Recommendation core

`internal/xalgorithm` is a Go translation of the published x-algorithm value
model and selection core, with Apache 2.0 attribution and source fixtures.
This is not a complete rewrite of the upstream project. The feed uses local
probability estimates instead of Phoenix inference, and administrator priority
and freshness adjustments. See `internal/xalgorithm/README.md` for precise
scope and defaults. Pagination snapshots preserve ranking and URT sort indexes
in the patched official client. Latest mode ignores all priority multipliers.

Ads have an advertiser, text, optional owned image, HTTP(S) destination, active
switch, schedule and a delivery limit. They are explicitly labeled “Реклама”
in Home feeds. Impression counts mean feed deliveries, not measured viewport
views. Clicks are deduplicated per delivery. No billing is attached.

`/web/reports` is an explicit FlashLarry extension for reporting posts; it is
**not** claimed to be an X endpoint. A small addition to the patched official
web client opens the report form. The existing X REST report-spam route reports
an account. Administrative HTTP routes belong to the separate admin service.

## Verification

`go test ./...` runs local tests. `internal/social/integration_test.go` requires
`FLASHLARRY_TEST_DATABASE_URL` containing `flashlarry_integration`, and checks
ownership, privacy, follow requests, blocks, moderation/audits, collections,
articles/notes, notification persistence, ad limits/click deduplication and
concurrent scheduled publication against an isolated Postgres database.
Production accounts are not used by those tests.

Runtime integration scenarios also cover priority vs chronological ordering,
cursor ownership and stable pagination, private/hidden/suspended authors,
restoration, manual Premium, encrypted SMTP credentials, verified email
registration through an isolated TLS SMTP server, one-use codes, mode-change
invalidation and preserved/rejected sessions. The SMTP test sends no external
email and uses Redis database 14 and an isolated Postgres database.

