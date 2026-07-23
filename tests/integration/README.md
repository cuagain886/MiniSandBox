# Integration tests

This suite will exercise sandbox lifecycle reconciliation against a real Docker
daemon. Tests should be opt-in and clean up every managed container, workspace,
and socket they create.

