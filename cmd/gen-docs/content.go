package main

// commandDoc is the hand-written discourse for one command. The generator adds
// the synopsis, options, and cross-references from the urfave/cli tree.
type commandDoc struct {
	Summary     string
	Paragraphs  []string
	Examples    []string
	Notes       []string
	Environment []string
}

var commandDocs = map[string]commandDoc{
	"perch": {
		Summary: "a small IMAP mailbox service backed by mxRaven",
		Paragraphs: []string{
			"perch runs a local IMAP server and authenticated submission endpoint, backed by mxRaven for inbound MX, outbound delivery, SPF/DKIM/DMARC, IP reputation, and spam filtering.",
			"Messages are stored in SQLite or offloaded to S3. The same binary provides administrative commands and a scriptable mail client for IMAP and SMTP.",
		},
		Examples: []string{
			"perch setup --workspace my-workspace --domain example.com --hostname localhost",
			"perch account add --email you@example.com",
			"perch serve --config ~/.local/share/perch/perch.yaml",
			"perch mail list --server 127.0.0.1:1143 --plaintext --username you@example.com",
		},
		Notes: []string{
			"Run `perch help <command>` or see the per-command manpages for details.",
			"The default configuration path is `$XDG_CONFIG_HOME/perch/perch.yaml`.",
			"perch does not need a public IP, port forwarding, or inbound SMTP ports.",
		},
	},
	"perch init": {
		Summary: "create a local configuration and optional first account",
		Paragraphs: []string{
			"init writes a self-contained perch.yaml and initializes the SQLite database without contacting mxRaven. It is intended for local testing or for deployments where mxRaven is provisioned separately.",
			"If --email is set, init also creates the first account and its default mailbox set. Use --self-signed to generate a local TLS certificate for LAN clients.",
		},
		Examples: []string{
			"perch init --data-dir ~/.local/share/perch --hostname localhost --email you@example.com",
			"perch init --self-signed --hostname mail.lan --email you@example.com",
		},
		Notes: []string{
			"The config is written with mode 0600 and the data directory with mode 0700.",
			"--force overwrites an existing configuration.",
		},
	},
	"perch setup": {
		Summary: "create the mxRaven domain, listeners, keys, and routes",
		Paragraphs: []string{
			"setup asks mxRaven for a device code, prints the dashboard approval address, and waits for a workspace owner to approve the perch connected application. After approval it provisions a workspace domain, MTA listener, optional submission listener, API keys, inbound route, sending-domain policy, and optional S3_STORE rule, then writes perch.yaml.",
			"The access token is held in memory for the setup run only and is never written to the config. Pass --token or set MXRAVEN_API_TOKEN to use a workspace-scoped API token instead of interactive approval. Listener and S3 secrets are written inline unless you replace them with *_env references.",
		},
		Examples: []string{
			"perch setup --config /etc/perch/perch.yaml --data-dir /var/lib/perch \\\n  --workspace my-workspace --domain example.com --hostname mail.example.com",
			"perch setup ... --no-browser",
			"perch setup ... --storage s3 --s3-bucket my-mail-archive --s3-region auto \\\n  --s3-endpoint https://<account>.r2.cloudflarestorage.com --s3-access-key <key>",
			"perch setup ... --public --acme-email admin@example.com --acme-domain mail.example.com",
		},
		Notes: []string{
			"Approval requires the workspace owner role. The workspace you approve must match --workspace.",
			"Publish the DNS records printed by the wizard, or pass --wait to wait for verification.",
			"Use --no-browser when the command runs over SSH or as a service user.",
			"Use --no-submission for inbound-only deployments.",
			"Rate-limit and policy flags map directly to mxRaven listener settings.",
		},
		Environment: []string{
			"MXRAVEN_API_TOKEN     legacy control-plane token for non-interactive setup",
			"MXRAVEN_ISSUER        mxRaven identity issuer URL",
			"MXRAVEN_DASHBOARD_URL mxRaven dashboard URL used for approval",
			"MXRAVEN_CLIENT_ID     registered perch connected-app client ID",
		},
	},
	"perch serve": {
		Summary: "run IMAP, submission, and the notification consumer",
		Paragraphs: []string{
			"serve runs in the foreground and starts the configured IMAP, IMAPS, submission, and SMTPS listeners plus the mxRaven WebSocket consumer and S3 reconciler.",
			"It logs to stderr, drains connections on SIGTERM/SIGINT for up to five seconds, and reloads certificate files on SIGHUP when tls.cert_file and tls.key_file are set. It does not fork, write a PID file, or support socket activation.",
		},
		Examples: []string{
			"perch serve --config /etc/perch/perch.yaml",
			"sudo systemctl reload perch   # only with manual TLS files",
		},
		Notes: []string{
			"Use Type=simple in systemd; Type=notify and socket units are not supported.",
			"SIGHUP terminates the process when manual TLS files are not configured.",
			"Account, alias, and mailbox changes apply without restarting serve.",
		},
	},
	"perch account": {
		Summary: "add, list, password, disable, enable, or delete accounts",
		Paragraphs: []string{
			"Manage local mailbox accounts. Creating an account also creates the default mailbox set: INBOX, Sent, Drafts, Trash, Junk, and Archive.",
			"Deleting an account removes its addresses, mailboxes, and messages. Outbound history is retained with the account reference cleared.",
		},
		Examples: []string{
			"perch account add --email you@example.com",
			"perch account list",
			"perch account passwd --email you@example.com",
			"perch account delete --email you@example.com",
		},
		Notes: []string{
			"Passwords are hashed with Argon2id.",
			"Account changes are written to SQLite and apply without restarting serve.",
			"Account deletion is immediate and cannot be undone. Local message blobs are released; S3 tombstones are left for gc.",
		},
	},
	"perch account add": {
		Summary: "create an account",
		Paragraphs: []string{
			"Create an account, its primary address, and default mailboxes in one transaction. If --password is omitted, perch prompts on a terminal.",
		},
		Examples: []string{
			"perch account add --email you@example.com --name 'Your Name'",
		},
		Notes: []string{
			"The account is enabled immediately.",
		},
	},
	"perch account list": {
		Summary: "list accounts",
		Paragraphs: []string{
			"Print account ID, primary address, display name, disabled state, and creation time.",
		},
		Examples: []string{
			"perch account list",
		},
	},
	"perch account passwd": {
		Summary: "change an account password",
		Paragraphs: []string{
			"Replace the account's Argon2id password hash. Existing IMAP and submission sessions are not terminated.",
		},
		Examples: []string{
			"perch account passwd --email you@example.com",
		},
		Notes: []string{
			"Prompts for the new password when --password is omitted.",
		},
	},
	"perch account disable": {
		Summary: "disable an account",
		Paragraphs: []string{
			"Disable prevents new IMAP and submission logins without deleting messages. Use enable to reverse it.",
		},
		Examples: []string{
			"perch account disable --email you@example.com",
		},
	},
	"perch account enable": {
		Summary: "enable an account",
		Paragraphs: []string{
			"Re-enable a previously disabled account.",
		},
		Examples: []string{
			"perch account enable --email you@example.com",
		},
	},
	"perch account delete": {
		Summary: "delete an account and all of its messages",
		Paragraphs: []string{
			"Delete an account, its primary address and aliases, its mailboxes, and every message they contain in one transaction. Outbound submission history is retained with its account reference cleared.",
			"Local message blobs are released immediately. S3-backed message rows are kept as tombstones so reconciliation does not re-ingest deleted mail; remove the remote objects separately with perch gc --s3-delete.",
		},
		Examples: []string{
			"perch account delete --email you@example.com",
			"perch account delete --email you@example.com --yes",
		},
		Notes: []string{
			"Prompts for confirmation unless --yes is set. Non-interactive callers must pass --yes.",
			"Account deletion is immediate and cannot be undone.",
		},
	},
	"perch alias": {
		Summary: "manage address aliases and catch-all domains",
		Paragraphs: []string{
			"Aliases map additional addresses to an account. A catch-all is stored as *@domain and accepts any local part for that domain.",
		},
		Examples: []string{
			"perch alias add --account you@example.com --address sales@example.com",
			"perch alias add --account you@example.com --catch-all --address example.net",
			"perch alias list --account you@example.com",
		},
		Notes: []string{
			"Plus-addressing (user+tag@domain) works without an explicit alias.",
		},
	},
	"perch alias add": {
		Summary: "add an alias or catch-all",
		Paragraphs: []string{
			"Add an exact address alias, or use --catch-all with a domain to route all unmatched addresses at that domain to the account. The address must not already belong to another account.",
		},
		Examples: []string{
			"perch alias add --account you@example.com --address sales@example.com",
			"perch alias add --account you@example.com --catch-all --address example.net",
		},
	},
	"perch alias remove": {
		Summary: "remove an alias or catch-all",
		Paragraphs: []string{
			"Remove an alias address or catch-all from the address table. Messages already delivered are unaffected.",
		},
		Examples: []string{
			"perch alias remove --address sales@example.com",
		},
	},
	"perch alias list": {
		Summary: "list aliases",
		Paragraphs: []string{
			"List the addresses and catch-all domains assigned to an account.",
		},
		Examples: []string{
			"perch alias list --account you@example.com",
		},
	},
	"perch mailbox": {
		Summary: "list mailboxes for an account",
		Paragraphs: []string{
			"Inspect mailbox metadata stored in SQLite. This reads the local database directly and does not require serve to be running.",
		},
		Examples: []string{
			"perch mailbox list --account you@example.com",
		},
		Notes: []string{
			"The mailbox command is local-only; use `perch mail list` for a remote IMAP server.",
		},
	},
	"perch mailbox list": {
		Summary: "list mailboxes",
		Paragraphs: []string{
			"Print each mailbox name, special-use attribute, and subscription state for an account.",
		},
		Examples: []string{
			"perch mailbox list --account you@example.com",
		},
	},
	"perch mail": {
		Summary: "list, search, fetch, send, and inspect mail over IMAP and SMTP",
		Paragraphs: []string{
			"The mail command group is a scriptable IMAP and SMTP client. list, search, and fetch talk IMAP; send talks SMTP; caps prints the SMTP EHLO extensions of a submission server.",
			"Credentials and TLS flags are shared. IMAP flags are local to list/search/fetch; SMTP flags are local to send/caps.",
		},
		Examples: []string{
			"perch mail list --server imaps://mail.example.com --username you@example.com",
			"perch mail search -q 'unseen from:alice' --json",
			"perch mail fetch -q 'from:alice' --format mbox > alice.mbox",
			"perch mail send --from you@example.com --to alice@example.com \\\n  --subject Hello --body 'Hi Alice'",
		},
		Notes: []string{
			"If --server or --smtp-server is omitted, the matching listener is taken from --config.",
			"Use --plaintext or --smtp-plaintext only for trusted loopback listeners.",
			"Password sources: --password-file, --password-stdin, PERCH_IMAP_PASSWORD, or an interactive prompt.",
		},
		Environment: []string{
			"PERCH_IMAP_USER      default account username",
			"PERCH_IMAP_PASSWORD  account password",
		},
	},
	"perch mail list": {
		Summary: "list mailboxes",
		Paragraphs: []string{
			"List mailboxes on the selected IMAP server. The default output is a table; --json writes one JSON object per line.",
		},
		Examples: []string{
			"perch mail list --server 127.0.0.1:1143 --plaintext --username you@example.com",
			"perch mail list --server imaps://mail.example.com --username you@example.com --json",
		},
		Notes: []string{
			"--subscribed requires server support for LIST-EXTENDED.",
		},
		Environment: []string{
			"PERCH_IMAP_USER      default account username",
			"PERCH_IMAP_PASSWORD  account password",
		},
	},
	"perch mail search": {
		Summary: "search message metadata",
		Paragraphs: []string{
			"Search a mailbox with IMAP SEARCH and print metadata for matching messages. Results are newest first by default; --reverse flips the order.",
			"Bare query words search the message text. Supported keys: from, to, cc, bcc, subject, body, text, header:Name=Value, since, before, on, sent-since, sent-before, sent-on, seen, unseen, answered, unanswered, flagged, unflagged, deleted, undeleted, draft, undraft, larger, smaller, uid, flag/keyword, and unflag/unkeyword.",
		},
		Examples: []string{
			"perch mail search -q 'from:alice since:2026-01-01'",
			"perch mail search -q 'unseen larger:1M' --json",
			"perch mail search --uids 12,14 --limit 0",
		},
		Notes: []string{
			"The default limit is 50; --limit 0 means unlimited.",
			"The mailbox is examined read-only, so search does not change flags.",
		},
		Environment: []string{
			"PERCH_IMAP_USER      default account username",
			"PERCH_IMAP_PASSWORD  account password",
		},
	},
	"perch mail fetch": {
		Summary: "fetch raw messages",
		Paragraphs: []string{
			"Fetch complete RFC 5322 messages selected by UID, query, or --all. The default format writes one .eml per message when --dest is a directory, or raw bytes to stdout for a single message.",
			"mbox writes a standard mbox stream. maildir creates tmp/new/cur and preserves message flags.",
		},
		Examples: []string{
			"perch mail fetch --uids 42 > message.eml",
			"perch mail fetch -q 'from:alice' --format mbox > alice.mbox",
			"perch mail fetch -q 'unseen' --format maildir --dest ~/Mail",
			"perch mail fetch -q 'unseen' --dest ./messages",
		},
		Notes: []string{
			"BODY.PEEK[] is used unless --mark-seen is set.",
			"Writing eml to stdout requires exactly one message.",
			"--dest is a directory for eml/maildir and a file for mbox.",
			"Existing files are not overwritten unless --overwrite is passed.",
		},
		Environment: []string{
			"PERCH_IMAP_USER      default account username",
			"PERCH_IMAP_PASSWORD  account password",
		},
	},
	"perch mail send": {
		Summary: "compose and send a message",
		Paragraphs: []string{
			"Compose a MIME message and submit it through SMTP. Text and HTML bodies are combined as multipart/alternative; attachments wrap the result in multipart/mixed.",
			"Bodies come from --body/--body-file and --html-body/--html-file, or stdin when it is not a terminal. Attachments are base64 encoded and content types are detected from the file extension or contents.",
		},
		Examples: []string{
			"perch mail send --from you@example.com --to alice@example.com \\\n  --subject Hello --body 'Hi Alice'",
			"echo 'Body from stdin' | perch mail send --from you@example.com \\\n  --to alice@example.com --subject Hello",
			"perch mail send --from you@example.com --to alice@example.com \\\n  --subject Report --body-file message.txt --html-file message.html \\\n  --attach report.pdf",
			"perch mail send ... --dry-run > message.eml",
		},
		Notes: []string{
			"Bodies are quoted-printable unless --8bitmime is set; attachments are base64.",
			"SMTPUTF8 is detected from non-ASCII envelope addresses and extra headers; force it with --smtputf8.",
			"Bcc recipients are envelope-only and never appear in headers.",
			"SIZE is sent automatically and checked against the server's advertised limit.",
		},
		Environment: []string{
			"PERCH_IMAP_USER      default account username and sender",
			"PERCH_IMAP_PASSWORD  account password",
		},
	},
	"perch mail caps": {
		Summary: "show the SMTP extensions advertised by a submission server",
		Paragraphs: []string{
			"Connect to the configured submission server, perform EHLO, and print the advertised extensions. No message is sent and no credentials are used.",
		},
		Examples: []string{
			"perch mail caps",
			"perch mail caps --smtp-server smtp.mxraven.email:587",
		},
		Notes: []string{
			"The local perch relay advertises PIPELINING, 8BITMIME, ENHANCEDSTATUSCODES, SIZE, SMTPUTF8, STARTTLS, and AUTH on loopback or TLS.",
			"It does not expose DSN, CHUNKING, BINARYMIME, REQUIRETLS, or DELIVERBY.",
		},
	},
	"perch doctor": {
		Summary: "check the database, TLS, S3, and mxRaven connectivity",
		Paragraphs: []string{
			"Run integrity and connectivity checks for SQLite/FTS5, TLS certificate files, DNS, S3, and configured mxRaven listeners. The command exits non-zero when a check fails.",
		},
		Examples: []string{
			"perch doctor --config /etc/perch/perch.yaml",
		},
		Notes: []string{
			"Run as the same user that runs serve so file permissions match.",
		},
	},
	"perch backup": {
		Summary: "write a consistent SQLite backup",
		Paragraphs: []string{
			"Use SQLite's online backup API to write a consistent copy while serve is running. The destination must not already exist.",
		},
		Examples: []string{
			"perch backup --config /etc/perch/perch.yaml /var/lib/perch/backups/perch-$(date +%F).db",
		},
		Notes: []string{
			"Safe alongside serve because SQLite WAL mode allows concurrent readers and writers.",
		},
	},
	"perch reconcile": {
		Summary: "force one S3 bucket reconciliation pass",
		Paragraphs: []string{
			"Scan the configured S3 bucket and bind objects written by mxRaven to local message metadata. Requires storage.mode=s3.",
		},
		Examples: []string{
			"perch reconcile --config /etc/perch/perch.yaml",
		},
		Notes: []string{
			"serve runs this at startup and every storage.s3.reconcile_every (default 5m).",
		},
	},
	"perch gc": {
		Summary: "remove orphan blobs, old dead letters, and S3 tombstones",
		Paragraphs: []string{
			"Remove orphaned local blobs, dead letters older than --dead-letter-age, enforce the S3 spool limit, and optionally delete S3 objects for deleted messages. An incremental vacuum runs by default.",
		},
		Examples: []string{
			"perch gc --config /etc/perch/perch.yaml --dry-run",
			"perch gc --config /etc/perch/perch.yaml --s3-delete",
		},
		Notes: []string{
			"--s3-delete is ignored in SQLite mode.",
			"Without --s3-delete, S3 tombstones accumulate.",
		},
	},
	"perch outbound": {
		Summary: "list submitted messages and delivery status",
		Paragraphs: []string{
			"Inspect the outbound table populated by the submission relay. It shows acceptance and later delivery attempts reported by mxRaven.",
		},
		Examples: []string{
			"perch outbound list --account you@example.com",
		},
	},
	"perch outbound list": {
		Summary: "list submitted messages",
		Paragraphs: []string{
			"Print recent outbound submissions, optionally filtered by account. --limit defaults to 100.",
		},
		Examples: []string{
			"perch outbound list --limit 20",
		},
	},
	"perch version": {
		Summary: "print the version",
		Paragraphs: []string{
			"Print the version embedded at build time, or the module version for go install builds.",
		},
		Examples: []string{
			"perch version",
		},
	},
}
