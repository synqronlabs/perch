---
layout: landing
title: perch
description: A single-binary IMAP and SMTP server with a POSIX CLI, by mxRaven.
---

<section class="hero">
  <div class="landing-wrap">
    <h1 class="hero-title" aria-label="Open source inbox for you, your agent, or your app">
      <span class="hero-title__lead" aria-hidden="true">Open source inbox for</span>
      <span class="hero-title__rotator" aria-hidden="true">
        <span class="hero-title__word" data-state="active">you</span>
        <span class="hero-title__word">your agent</span>
        <span class="hero-title__word">your app</span>
      </span>
    </h1>
    <div class="hero-actions">
      <a class="button button--primary" href="{{< relref "/docs/getting-started/installation" >}}">Get started</a>
      <a class="button button--secondary" href="https://tryperch.mxraven.com/">Live demo</a>
    </div>
    <button class="install" type="button" aria-label="Copy install command">
      <span class="prompt" aria-hidden="true">$</span>
      <code>go install github.com/synqronlabs/perch/cmd/perch@latest</code>
      <span class="install__action" aria-hidden="true">Copy</span>
    </button>
    <span class="sr-only" role="status" aria-live="polite" data-copy-status></span>
  </div>
</section>

<section class="section" id="features">
  <div class="landing-wrap">
    <div class="section-head">
      <h2>Mail infrastructure without the infrastructure.</h2>
      <p>
        perch is a mailbox server, a mail client, and a small automation surface
        in one binary. <a href="https://mxraven.com">mxRaven</a> handles the public edge: MX, SPF/DKIM/DMARC, IP
        reputation, and spam filtering.
      </p>
    </div>
    <div class="feature-grid">
      <article class="feature-card">
        <span class="feature-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24"><path d="M4 7h16v10H4z"></path><path d="m4 7 8 6 8-6"></path></svg>
        </span>
        <h3>1 file, zero sidecars</h3>
        <p>A CGO-free Go binary with embedded SQLite and FTS5. No Postfix, Dovecot, Rspamd, or container orchestra.</p>
      </article>
      <article class="feature-card">
        <span class="feature-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"></circle><path d="M3 12h18"></path><path d="M12 3c2.5 2.5 2.5 15 0 18"></path><path d="M12 3c-2.5 2.5-2.5 15 0 18"></path></svg>
        </span>
        <h3>No public IP required</h3>
        <p>Inbound mail arrives over an outbound WebSocket. Works behind NAT, CGNAT, or a homelab router.</p>
      </article>
      <article class="feature-card">
        <span class="feature-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="8" ry="3"></ellipse><path d="M4 5v7c0 1.7 3.6 3 8 3s8-1.3 8-3V5"></path><path d="M4 12v7c0 1.7 3.6 3 8 3s8-1.3 8-3v-7"></path></svg>
        </span>
        <h3>Your storage, your rules</h3>
        <p>Keep compressed mail in SQLite or offload raw .eml files to S3/R2 while metadata and search stay local.</p>
      </article>
      <article class="feature-card">
        <span class="feature-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24"><path d="m5 7 5 5-5 5"></path><path d="m13 17 6 0"></path></svg>
        </span>
        <h3>Agent-ready CLI</h3>
        <p>POSIX commands with NDJSON, raw .eml, mbox, Maildir, and real exit codes. Easy to wire into an agent loop.</p>
      </article>
      <article class="feature-card">
        <span class="feature-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24"><path d="M7 7h11l-3-3"></path><path d="M17 17H6l3 3"></path></svg>
        </span>
        <h3>Standard protocols</h3>
        <p>IMAP and authenticated SMTP. Use Thunderbird, Apple Mail, mutt, or your own application client.</p>
      </article>
      <article class="feature-card">
        <span class="feature-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24"><path d="M12 3 5 6v5c0 4.5 3 8.4 7 10 4-1.6 7-5.5 7-10V6l-7-3Z"></path><path d="m9 12 2 2 4-4"></path></svg>
        </span>
        <h3>Private by default</h3>
        <p>Self-hosted mail with zero inbound SMTP ports. <a href="https://mxraven.com">mxRaven</a> does not retain your messages.</p>
      </article>
    </div>
  </div>
</section>

<section class="section section--agent" id="agents">
  <div class="landing-wrap agent">
    <div class="agent-copy">
      <h2>Give your agent a real inbox.</h2>
      <p>
        No browser sessions, no scraping, no vendor-specific mailbox API. Install
        the bundled Agent Skill, and your agent gets normal email plus a clean
        command-line interface designed for scripts and tool calls.
      </p>
      <ul class="checks">
        <li>Install the Agent Skill for Pi, Hermes Agent, and other harnesses</li>
        <li>Search and list messages as NDJSON for structured context</li>
        <li>Fetch raw .eml for RAG, summarization, or archival</li>
        <li>Send replies and attachments with one command</li>
        <li>Use mbox or Maildir when a pipeline expects files</li>
        <li>Works locally or against any remote IMAP/SMTP server</li>
      </ul>
      <p class="agent-link"><a href="{{< relref "/docs/mail/agents" >}}">Read the agent guide →</a></p>
    </div>
    <div class="terminal" aria-label="Example agent mail commands">
      <div class="terminal-bar" aria-hidden="true"><span></span><span></span><span></span></div>
      <pre><code><span class="comment"># Install the agent skill</span>
<span class="prompt">$</span> npx skills add https://github.com/synqronlabs/perch \
    --skill perch
<br>
<span class="comment"># Give the agent structured context</span>
<span class="prompt">$</span> perch mail search -q 'unseen from:alice' --json
<br>
<span class="comment"># Fetch the full message</span>
<span class="prompt">$</span> perch mail fetch --uids 42 &gt; message.eml
<br>
<span class="comment"># Reply with an attachment</span>
<span class="prompt">$</span> perch mail send --to alice@example.com \
    --subject 'Report ready' \
    --body 'See attached.' \
    --attach report.pdf
</code></pre>
    </div>
  </div>
</section>
