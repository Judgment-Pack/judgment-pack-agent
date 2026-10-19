# Security policy

## Reporting a vulnerability

Do not open a public issue for a vulnerability. Use GitHub's private vulnerability reporting for
this repository (Security -> Report a vulnerability), or open a minimal non-sensitive issue asking
a maintainer to establish a private channel.

Report, for example:

- a way for a model to call a tool the host did not offer, or to act without the approval the
  host requires;
- a model key, credential or token reaching a model request, a tool result, a log or a checkpoint;
- an adapter that runs a tool its contract marks as needing approval without pausing;
- retrieved page text that steers a tool call the person did not ask for, when the contract says
  the adapter must frame it as data;
- a way past the certification suite that lets an adapter claim conformance it does not have.

Include the adapter, the contract version and a reproduction. We confirm receipt within five
working days.

## Scope

The harness never holds model keys; the host's relay does. A framework's own guardrails are not a
security boundary here, and a report that only shows a framework editing its own instructions is
out of scope unless it reaches a tool or a credential.
