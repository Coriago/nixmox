# Final acceptance testing

For application changes, extend `e2e/run.py` with coverage for the affected user
workflows and run the comprehensive three-node E2E (both parts) as the main final
acceptance test. Unit tests, browser checks, and `nix/checks/` are complementary;
they do not replace deployment, restart, quorum loss/recovery, removal, and reuse.

Test the changed source, not an older published revision. Use the explicit
working-tree snapshot mode for unpublished changes, or the GitHub mode after an
authorized publication. Never publish to main merely to run a test. Record the
source mode, tested SHA, final checkpoint, and artifact directory in the report.
If the comprehensive run fails or cannot finish, state that clearly and retain
failure evidence; do not describe lower-level tests as comprehensive acceptance.
