# Future ideas (not implemented)

The current scope is extracting recorded conversations from local AI applications.

- Web sessions: investigate an official export API or another supported export
  mechanism for conversation history, attachments and recorded tool context.
  Keep account authorization explicit. Do not infer browser sandbox access from
  the ability to export messages.
- Remote sandboxes: investigate an opt-in system instruction that starts an
  operator-accessible endpoint inside each sandbox, if the host supports it.
  Authentication, network reachability, lifetime and permissions need separate
  design work. This is an idea to assess, not part of the current utility.
- Semantic pruning: detect obsolete tool observations and superseded facts while
  preserving the exact requirements and decision wording, with a reviewable
  deletion plan. Current compact mode only replaces exact duplicates; older
  tool results are removed solely under an explicit cutoff option.
- Additional Cursor storage generations: decode proprietary binary agentKv
  records when a verifiable schema and redistributable fixtures are available.
